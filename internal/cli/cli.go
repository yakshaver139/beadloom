package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joshharrison/beadloom/internal/bd"
	"github.com/joshharrison/beadloom/internal/claude"
	"github.com/joshharrison/beadloom/internal/cpm"
	"github.com/joshharrison/beadloom/internal/graph"
	"github.com/joshharrison/beadloom/internal/orchestrator"
	"github.com/joshharrison/beadloom/internal/planner"
	"github.com/joshharrison/beadloom/internal/reporter"
	"github.com/joshharrison/beadloom/internal/state"
	"github.com/joshharrison/beadloom/internal/ui"
	"github.com/joshharrison/beadloom/internal/viewer"
	"github.com/joshharrison/beadloom/internal/worktree"
	"github.com/spf13/cobra"
)

var (
	flagDB             string
	flagMaxParallel    int
	flagSafe           bool
	flagDryRun         bool
	flagFilter         string
	flagTimeout        string
	flagWorktreeDir    string
	flagPromptTemplate string
	flagJSON           bool
	flagOutput         string
	flagPlanFile       string
	flagWatch          bool
	flagLogs           string
	flagForce          bool
	flagFormat         string
)

// Execute is the single entry point for the CLI. It derives the binary name
// from os.Args[0] so that help text is correct for both "beadloom" and "bdl".
func Execute() {
	binName := filepath.Base(os.Args[0])

	var alias string
	switch binName {
	case "bdl":
		alias = "beadloom"
	default:
		binName = "beadloom"
		alias = "bdl"
	}

	rootCmd := &cobra.Command{
		Use:   binName,
		Short: "Orchestrate parallel task execution across Claude Code sessions",
		Long: fmt.Sprintf(`%s reads a task graph from a Beads database, computes critical paths
and parallelizable work, then spawns multiple Claude Code sessions across
git worktrees to execute tasks concurrently.

Alias: this command is also available as %q.`, binName, alias),
	}

	// Global flags
	rootCmd.PersistentFlags().StringVar(&flagDB, "db", "", "Beads database path")
	rootCmd.PersistentFlags().IntVar(&flagMaxParallel, "max-parallel", 4, "Max concurrent agent sessions")
	rootCmd.PersistentFlags().BoolVar(&flagSafe, "safe", false, "Do NOT pass --dangerously-skip-permissions to Claude")
	rootCmd.PersistentFlags().StringVar(&flagTimeout, "timeout", "30m", "Per-task timeout")
	rootCmd.PersistentFlags().StringVar(&flagWorktreeDir, "worktree-dir", ".worktrees", "Directory for worktrees")
	rootCmd.PersistentFlags().StringVar(&flagPromptTemplate, "prompt-template", "", "Custom agent prompt template path")
	rootCmd.PersistentFlags().BoolVar(&flagJSON, "json", false, "Machine-readable JSON output")

	rootCmd.AddCommand(planCmd())
	rootCmd.AddCommand(runCmd())
	rootCmd.AddCommand(statusCmd())
	rootCmd.AddCommand(cancelCmd())
	rootCmd.AddCommand(vizCmd())
	rootCmd.AddCommand(viewCmd())
	rootCmd.AddCommand(inferDepsCmd())
	rootCmd.AddCommand(mergeCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// buildPlan is shared logic for plan and run commands.
func buildPlan() (*planner.ExecutionPlan, *graph.TaskGraph, *cpm.CPMResult, error) {
	client := bd.NewClient("", flagDB)

	g, err := graph.Build(client)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("build task graph: %w", err)
	}

	if g.TaskCount() == 0 {
		return nil, nil, nil, fmt.Errorf("no open tasks found")
	}

	// Apply filter if specified
	if flagFilter != "" {
		g, err = applyFilter(g, flagFilter)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("apply filter: %w", err)
		}
	}

	result, err := cpm.Analyze(g)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("CPM analysis: %w", err)
	}

	config := planner.PlanConfig{
		MaxParallel:        flagMaxParallel,
		Safe:               flagSafe,
		TimeoutPerTask:     flagTimeout,
		WorktreeDir:        flagWorktreeDir,
		PromptTemplatePath: flagPromptTemplate,
		DbPath:             flagDB,
	}

	plan, err := planner.Generate(g, result, config)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generate plan: %w", err)
	}

	return plan, g, result, nil
}

func planCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plan",
		Short: "Analyze task graph and compute execution plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, g, result, err := buildPlan()
			if err != nil {
				return err
			}

			if flagJSON {
				return outputJSON(plan)
			}

			if flagOutput != "" {
				data, err := json.MarshalIndent(plan, "", "  ")
				if err != nil {
					return err
				}
				return os.WriteFile(flagOutput, data, 0644)
			}

			printPlan(plan, g, result)
			return nil
		},
	}

	cmd.Flags().StringVar(&flagFilter, "filter", "", "Filter tasks (e.g., priority<=1, label=backend)")
	cmd.Flags().StringVar(&flagOutput, "output", "", "Save plan to file")
	cmd.Flags().BoolVar(&flagDryRun, "dry-run", false, "Show plan without executing")

	return cmd
}

func runCmd() *cobra.Command {
	var flagQuiet bool
	var flagAutomerge bool
	var flagGitTrace bool

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Execute the plan (or plan + run in one shot)",
		RunE: func(cmd *cobra.Command, args []string) error {
			var plan *planner.ExecutionPlan

			if flagPlanFile != "" {
				data, err := os.ReadFile(flagPlanFile)
				if err != nil {
					return fmt.Errorf("read plan file: %w", err)
				}
				plan = &planner.ExecutionPlan{}
				if err := json.Unmarshal(data, plan); err != nil {
					return fmt.Errorf("parse plan file: %w", err)
				}
			} else {
				var err error
				plan, _, _, err = buildPlan()
				if err != nil {
					return err
				}
			}

			if flagDryRun {
				if flagJSON {
					return outputJSON(plan)
				}
				fmt.Printf("🎯 %s\n", ui.Yellow("Dry run — plan generated but not executed."))
				fmt.Printf("Would execute %s tasks in %s waves (max %d parallel)\n",
					ui.Bold(plan.TotalTasks), ui.Bold(plan.TotalWaves), plan.Config.MaxParallel)
				for _, wave := range plan.Waves {
					fmt.Printf("  🌊 %s %d: %d tasks\n", ui.BoldWhite("Wave"), wave.Index+1, len(wave.Tasks))
					for _, t := range wave.Tasks {
						crit := ""
						if t.IsCritical {
							crit = " " + ui.BoldYellow("⚡")
						}
						fmt.Printf("    %s  %s%s\n", ui.BoldMagenta(t.TaskID), t.Title, crit)
					}
				}
				return nil
			}

			// Setup signal handling
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			go func() {
				<-sigCh
				fmt.Fprintf(os.Stderr, "\n🛑 %s\n", ui.Yellow("Received interrupt, cancelling..."))
				cancel()
			}()

			timeout, err := time.ParseDuration(flagTimeout)
			if err != nil {
				return fmt.Errorf("parse timeout: %w", err)
			}

			client := bd.NewClient("", flagDB)
			wm := worktree.NewManager(flagWorktreeDir, client)

			orch := orchestrator.New(plan, wm, orchestrator.Config{
				MaxParallel:    flagMaxParallel,
				Safe:           flagSafe,
				Quiet:          flagQuiet,
				Automerge:      flagAutomerge,
				GitTrace:       flagGitTrace,
				TimeoutPerTask: timeout,
				WorktreeDir:    flagWorktreeDir,
				DbPath:         flagDB,
			})

			if !flagJSON {
				ui.PrintLogo()
			}
			fmt.Printf("🚀 %s executing %s tasks in %s waves\n",
				ui.BoldCyan("Beadloom:"), ui.Bold(plan.TotalTasks), ui.Bold(plan.TotalWaves))

			if err := orch.Run(ctx); err != nil {
				rpt := reporter.New(plan, orch.State)
				fmt.Fprintln(os.Stderr, rpt.Summary())
				return err
			}

			rpt := reporter.New(plan, orch.State)
			fmt.Println(rpt.Summary())
			return nil
		},
	}

	cmd.Flags().StringVar(&flagPlanFile, "plan", "", "Load plan from file")
	cmd.Flags().BoolVar(&flagDryRun, "dry-run", false, "Show plan without executing")
	cmd.Flags().StringVar(&flagFilter, "filter", "", "Filter tasks")
	cmd.Flags().BoolVarP(&flagQuiet, "quiet", "q", false, "Suppress streaming agent output")
	cmd.Flags().BoolVar(&flagAutomerge, "automerge", false, "Merge branches at wave boundaries (wave-barrier mode)")
	cmd.Flags().BoolVar(&flagGitTrace, "git-trace", false, "Log every git command and its output to stderr")

	return cmd
}

func statusCmd() *cobra.Command {
	var flagPrevious bool
	var flagPlanID string

	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show running sessions and progress",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Validate flag combinations
			if flagWatch && (flagPrevious || flagPlanID != "") {
				return fmt.Errorf("--watch cannot be combined with --previous or --plan (historical runs aren't running)")
			}
			if flagPrevious && flagPlanID != "" {
				return fmt.Errorf("--previous and --plan are mutually exclusive")
			}

			var st *state.RunState
			var plan *planner.ExecutionPlan

			switch {
			case flagPrevious:
				var err error
				st, plan, err = state.LoadPrevious()
				if err != nil {
					return fmt.Errorf("load previous run: %w", err)
				}

			case flagPlanID != "":
				var err error
				st, err = state.LoadArchived(flagPlanID)
				if err != nil {
					return fmt.Errorf("load archived run %s: %w", flagPlanID, err)
				}
				plan, err = state.LoadArchivedPlan(flagPlanID)
				if err != nil {
					return fmt.Errorf("load archived plan %s: %w", flagPlanID, err)
				}

			default:
				if !state.Exists() {
					return fmt.Errorf("no active beadloom run (no .beadloom/state.json found)")
				}

				var err error
				st, err = state.Load()
				if err != nil {
					return err
				}

				// Try to load the persisted plan first, fall back to rebuilding from beads
				plan, err = state.LoadPlan()
				if err != nil {
					plan, _, _, err = buildPlan()
					if err != nil {
						// Fall back to basic status from state only
						if flagJSON {
							return outputJSON(st)
						}
						fmt.Printf("🧵 %s %s\n", ui.BoldCyan("Beadloom Run:"), ui.Dim(st.PlanID))
						fmt.Printf("Status: %s\n", ui.Bold(st.Status))
						fmt.Printf("Wave: %d/%d\n", st.CurrentWave+1, st.TotalWaves)
						fmt.Printf("Sessions: %d\n", len(st.Sessions))
						for id, ss := range st.Sessions {
							fmt.Printf("  %s %s: %s\n", ui.StatusIcon(string(ss.Status)), ui.BoldMagenta(id), ss.Status)
						}
						return nil
					}
				}
			}

			rpt := reporter.New(plan, st)

			if flagLogs != "" {
				ss := st.GetSession(flagLogs)
				if ss == nil {
					return fmt.Errorf("no session found for task %s", flagLogs)
				}
				data, err := os.ReadFile(ss.LogFile)
				if err != nil {
					return fmt.Errorf("read log: %w", err)
				}
				fmt.Print(string(data))
				return nil
			}

			if flagJSON {
				data, err := rpt.JSON()
				if err != nil {
					return err
				}
				fmt.Println(string(data))
				return nil
			}

			if flagWatch {
				for {
					fmt.Print("\033[2J\033[H") // clear screen
					rpt.PrintStatus(os.Stdout)
					time.Sleep(5 * time.Second)

					// Reload state
					var err error
					st, err = state.Load()
					if err != nil {
						return err
					}
					rpt = reporter.New(plan, st)

					if st.Status != "running" {
						rpt.PrintStatus(os.Stdout)
						break
					}
				}
				return nil
			}

			rpt.PrintStatus(os.Stdout)
			return nil
		},
	}

	cmd.Flags().BoolVar(&flagWatch, "watch", false, "Watch mode (refresh every 5s)")
	cmd.Flags().StringVar(&flagLogs, "logs", "", "Show logs for a specific task")
	cmd.Flags().BoolVar(&flagPrevious, "previous", false, "Show status of the most recent completed run")
	cmd.Flags().StringVar(&flagPlanID, "plan", "", "Show status of a specific historical run by plan ID")

	return cmd
}

func cancelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cancel [task-id]",
		Short: "Abort running sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !state.Exists() {
				return fmt.Errorf("no active beadloom run")
			}

			st, err := state.Load()
			if err != nil {
				return err
			}

			pids := st.ActivePIDs()
			if len(pids) == 0 {
				fmt.Printf("%s No running sessions to cancel.\n", ui.Dim("🛑"))
				return nil
			}

			sig := syscall.SIGTERM
			if flagForce {
				sig = syscall.SIGKILL
			}

			if len(args) > 0 {
				// Cancel specific task
				taskID := args[0]
				ss := st.GetSession(taskID)
				if ss == nil {
					return fmt.Errorf("no session found for task %s", taskID)
				}
				if ss.PID > 0 {
					proc, err := os.FindProcess(ss.PID)
					if err == nil {
						proc.Signal(sig)
						fmt.Printf("🛑 Sent %s to task %s (PID %d)\n", sig, ui.BoldMagenta(taskID), ss.PID)
					}
				}
			} else {
				// Cancel all
				for _, pid := range pids {
					proc, err := os.FindProcess(pid)
					if err == nil {
						proc.Signal(sig)
					}
				}
				fmt.Printf("🛑 Sent %s to %s running sessions\n", sig, ui.Bold(len(pids)))
			}

			st.SetStatus("cancelled")
			return nil
		},
	}

	cmd.Flags().BoolVar(&flagForce, "force", false, "Force kill (SIGKILL)")

	return cmd
}

func mergeCmd() *cobra.Command {
	var flagNoSquash bool
	var flagNoCleanup bool

	cmd := &cobra.Command{
		Use:   "merge",
		Short: "Merge completed worktree branches and clean up",
		Long: `Merges all beadloom/* branches back into the current branch, then
deletes the branches and cleans up worktree state.

By default uses squash merges for a clean history. Use --no-squash for
regular merge commits.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client := bd.NewClient("", flagDB)
			wm := worktree.NewManager(flagWorktreeDir, client)

			// 1. Find beadloom/* branches
			branches, err := wm.ListBranches()
			if err != nil {
				return fmt.Errorf("list branches: %w", err)
			}
			if len(branches) == 0 {
				fmt.Println("No beadloom/* branches found — nothing to merge.")
				return nil
			}

			// 2. Dry-run: just print and return
			if flagDryRun {
				if flagJSON {
					return outputJSON(map[string]interface{}{
						"branches": branches,
						"mode":     mergeMode(flagNoSquash),
						"dry_run":  true,
					})
				}
				fmt.Printf("🔍 %s Found %s beadloom branches to merge (%s):\n\n",
					ui.BoldCyan("Dry run:"), ui.Bold(len(branches)), mergeMode(flagNoSquash))
				for _, b := range branches {
					fmt.Printf("  %s %s\n", ui.Cyan("→"), ui.BoldMagenta(b))
				}
				return nil
			}

			fmt.Printf("🧵 Merging %s branches (%s)...\n\n", ui.Bold(len(branches)), mergeMode(flagNoSquash))

			// 3. Merge each branch
			type mergeResult struct {
				Branch  string `json:"branch"`
				TaskID  string `json:"task_id"`
				Status  string `json:"status"`
				Message string `json:"message,omitempty"`
			}
			var results []mergeResult

			for _, branch := range branches {
				taskID := strings.TrimPrefix(branch, "beadloom/")

				// Build commit message from bd show
				subject := fmt.Sprintf("beadloom: %s", taskID)
				body := ""
				showOutput, err := client.ShowHuman(taskID)
				if err == nil && strings.TrimSpace(showOutput) != "" {
					// Parse first line as title
					lines := strings.SplitN(strings.TrimSpace(showOutput), "\n", 2)
					if len(lines) > 0 {
						title := strings.TrimSpace(lines[0])
						if title != "" {
							subject = fmt.Sprintf("beadloom: %s — %s", taskID, title)
						}
					}
					body = strings.TrimSpace(showOutput)
				}

				commitMsg := subject
				if body != "" {
					commitMsg = subject + "\n\n" + body
				}

				if flagNoSquash {
					// Regular merge with merge commit
					mergeCmd := exec.Command("git", "merge", "--no-ff", branch, "-m", commitMsg)
					out, mergeErr := mergeCmd.CombinedOutput()
					if mergeErr != nil {
						// Abort the failed merge
						exec.Command("git", "merge", "--abort").Run()
						errMsg := fmt.Sprintf("merge conflict on %s: %s", branch, strings.TrimSpace(string(out)))
						results = append(results, mergeResult{Branch: branch, TaskID: taskID, Status: "conflict", Message: errMsg})
						if flagJSON {
							return outputJSON(map[string]interface{}{"results": results, "error": errMsg})
						}
						fmt.Printf("  %s %s — merge conflict\n", ui.Red("✗"), ui.BoldMagenta(branch))
						fmt.Printf("\n%s Stopped at %s due to conflict. Resolve manually.\n", ui.BoldRed("Error:"), branch)
						return fmt.Errorf("merge conflict on branch %s", branch)
					}
				} else {
					// Squash merge + explicit commit
					squashCmd := exec.Command("git", "merge", "--squash", branch)
					out, mergeErr := squashCmd.CombinedOutput()
					if mergeErr != nil {
						exec.Command("git", "merge", "--abort").Run()
						errMsg := fmt.Sprintf("merge conflict on %s: %s", branch, strings.TrimSpace(string(out)))
						results = append(results, mergeResult{Branch: branch, TaskID: taskID, Status: "conflict", Message: errMsg})
						if flagJSON {
							return outputJSON(map[string]interface{}{"results": results, "error": errMsg})
						}
						fmt.Printf("  %s %s — merge conflict\n", ui.Red("✗"), ui.BoldMagenta(branch))
						fmt.Printf("\n%s Stopped at %s due to conflict. Resolve manually.\n", ui.BoldRed("Error:"), branch)
						return fmt.Errorf("merge conflict on branch %s", branch)
					}

					// Stage beads state so it's included in the commit
					exec.Command("git", "add", ".beads/").Run()

					// Check if the squash merge staged anything
					if err := exec.Command("git", "diff", "--cached", "--quiet").Run(); err == nil {
						// Nothing staged — branch changes already on main
						fmt.Printf("  %s %s — already up to date\n", ui.Dim("‣"), ui.BoldMagenta(branch))
						results = append(results, mergeResult{Branch: branch, TaskID: taskID, Status: "skipped", Message: "already up to date"})
						continue
					}

					commitCmd := exec.Command("git", "commit", "-m", commitMsg)
					if out, err := commitCmd.CombinedOutput(); err != nil {
						fmt.Printf("  %s %s — commit failed: %s\n", ui.Yellow("⚠"), ui.BoldMagenta(branch), strings.TrimSpace(string(out)))
						results = append(results, mergeResult{Branch: branch, TaskID: taskID, Status: "skipped", Message: "commit failed"})
						continue
					}
				}

				fmt.Printf("  %s %s\n", ui.Green("✓"), ui.BoldMagenta(branch))
				results = append(results, mergeResult{Branch: branch, TaskID: taskID, Status: "merged"})
			}

			// 4. Cleanup (unless --no-cleanup)
			// Only clean up branches that were successfully merged — keep skipped/failed ones
			if !flagNoCleanup {
				fmt.Printf("\n🧹 Cleaning up...\n")
				mergedBranches := make(map[string]bool)
				for _, r := range results {
					if r.Status == "merged" {
						mergedBranches[r.Branch] = true
					}
				}
				// Remove worktree directories for merged branches
				for branch := range mergedBranches {
					taskID := strings.TrimPrefix(branch, "beadloom/")
					wtPath := wm.Path(taskID)
					exec.Command("git", "worktree", "remove", "--force", wtPath).Run()
				}
				exec.Command("git", "worktree", "prune").Run()
				// Delete only merged branches
				for branch := range mergedBranches {
					if err := wm.DeleteBranch(branch); err != nil {
						fmt.Printf("  %s delete branch %s: %v\n", ui.Yellow("⚠"), branch, err)
					}
				}
				// Only remove state dirs if everything was merged
				if len(mergedBranches) == len(branches) {
					os.RemoveAll(flagWorktreeDir)
					state.CleanCurrent() // remove current run files, preserve history
				}
				// Sync bd worktree state
				client.Sync()

				// Warn about branches that weren't merged
				var skipped []string
				for _, r := range results {
					if r.Status != "merged" {
						skipped = append(skipped, r.Branch)
					}
				}
				if len(skipped) > 0 {
					fmt.Printf("\n%s %d branches were not merged and have been preserved:\n",
						ui.Yellow("⚠"), len(skipped))
					for _, b := range skipped {
						fmt.Printf("  %s %s\n", ui.Dim("→"), ui.BoldMagenta(b))
					}
				}
			}

			// 5. Summary
			merged := 0
			for _, r := range results {
				if r.Status == "merged" {
					merged++
				}
			}

			if flagJSON {
				return outputJSON(map[string]interface{}{
					"results": results,
					"merged":  merged,
					"total":   len(branches),
				})
			}

			suffix := " Branches and worktrees cleaned up."
			if flagNoCleanup {
				suffix = ""
			}
			fmt.Printf("\n🏁 Merged %s/%d branches.%s\n",
				ui.BoldGreen(merged), len(branches), suffix)
			return nil
		},
	}

	cmd.Flags().BoolVar(&flagNoSquash, "no-squash", false, "Use regular merge commits instead of squash")
	cmd.Flags().BoolVar(&flagNoCleanup, "no-cleanup", false, "Skip worktree and branch cleanup after merging")
	cmd.Flags().BoolVar(&flagDryRun, "dry-run", false, "Show what would be merged without merging")

	return cmd
}

func mergeMode(noSquash bool) string {
	if noSquash {
		return "no-ff merge"
	}
	return "squash"
}

func vizCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "viz",
		Short: "Print ASCII DAG of the execution plan",
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, g, result, err := buildPlan()
			if err != nil {
				return err
			}

			if flagFormat == "dot" {
				return printDOT(g, result)
			}

			printASCIIDAG(plan, g)
			return nil
		},
	}

	cmd.Flags().StringVar(&flagFormat, "format", "ascii", "Output format (ascii, dot)")
	cmd.Flags().StringVar(&flagFilter, "filter", "", "Filter tasks")

	return cmd
}

func viewCmd() *cobra.Command {
	var (
		flagPort   int
		flagNoOpen bool
	)

	cmd := &cobra.Command{
		Use:   "view",
		Short: "Open interactive browser visualiser for the execution plan",
		Long: `Builds the execution plan from the beads database, starts an
embedded HTTP server with the visualiser UI, POSTs the plan graph,
and opens the UI in your browser.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			plan, _, _, err := buildPlan()
			if err != nil {
				return err
			}

			addr := fmt.Sprintf("localhost:%d", flagPort)
			baseURL := fmt.Sprintf("http://%s", addr)

			if !viewer.IsPortOpen(addr) {
				url, err := viewer.Start(flagPort)
				if err != nil {
					return fmt.Errorf("start viewer: %w", err)
				}
				fmt.Printf("🖥️  Started viewer on %s\n", url)
			} else {
				fmt.Printf("🖥️  Viewer already running on %s\n", baseURL)
			}

			if err := viewer.PostPlan(baseURL, plan); err != nil {
				return err
			}
			fmt.Printf("✅ Plan sent to visualiser\n")

			if !flagNoOpen {
				openBrowser(baseURL)
				fmt.Printf("🌐 Opened %s\n", baseURL)
			} else {
				fmt.Printf("🌐 Open %s in your browser\n", baseURL)
			}

			fmt.Printf("Press Ctrl+C to stop the server.\n")
			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			<-sigCh
			fmt.Println()

			return nil
		},
	}

	cmd.Flags().IntVar(&flagPort, "port", 7171, "Viewer server port")
	cmd.Flags().BoolVar(&flagNoOpen, "no-open", false, "Skip opening browser")
	cmd.Flags().StringVar(&flagFilter, "filter", "", "Filter tasks before viewing")

	return cmd
}

// openBrowser opens the given URL in the default browser.
func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		cmd = exec.Command("cmd", "/c", "start", url)
	}
	cmd.Start()
}

// --- Output helpers ---

func outputJSON(v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func printPlan(plan *planner.ExecutionPlan, g *graph.TaskGraph, result *cpm.CPMResult) {
	blocked := 0
	for _, t := range g.Tasks {
		if len(g.RevAdj[t.ID]) > 0 {
			blocked++
		}
	}

	maxWaveWidth := 0
	for _, w := range plan.Waves {
		if len(w.Tasks) > maxWaveWidth {
			maxWaveWidth = len(w.Tasks)
		}
	}

	fmt.Printf("🎯 %s\n", ui.BoldCyan("Beadloom Execution Plan"))
	fmt.Println(ui.Cyan("═══════════════════════════"))
	fmt.Println()
	fmt.Printf("Tasks:     %s open, %s blocked\n", ui.Bold(g.TaskCount()), ui.Bold(blocked))
	fmt.Printf("⚡ Critical path: %s (%d tasks, est. %d units)\n",
		ui.BoldYellow(strings.Join(result.CriticalPath, " → ")), len(result.CriticalPath), result.TotalDuration)
	fmt.Printf("Waves:     %s\n", ui.Bold(plan.TotalWaves))
	fmt.Printf("Parallel:  %s (%d tasks in widest wave)\n", ui.Bold(plan.Config.MaxParallel), maxWaveWidth)
	fmt.Println()

	for _, wave := range plan.Waves {
		depStr := ui.Dim("independent")
		if wave.Index > 0 {
			depStr = ui.Dim(fmt.Sprintf("after wave %d", wave.Index))
		}
		fmt.Printf("🌊 %s %d (%d tasks, %s):\n", ui.BoldWhite("Wave"), wave.Index+1, len(wave.Tasks), depStr)
		for _, t := range wave.Tasks {
			crit := ""
			if t.IsCritical {
				crit = "  " + ui.BoldYellow("⚡ critical")
			}
			fmt.Printf("  %s  %s%s\n", ui.BoldMagenta(t.TaskID), t.Title, crit)
		}
		fmt.Println()
	}
}

func printASCIIDAG(plan *planner.ExecutionPlan, g *graph.TaskGraph) {
	fmt.Printf("🔗 %s\n", ui.BoldCyan("Task Dependency Graph"))
	fmt.Println(ui.Cyan("═══════════════════════"))
	fmt.Println()

	for _, wave := range plan.Waves {
		fmt.Printf("%s 🌊 Wave %d %s\n", ui.Cyan("──"), wave.Index+1, ui.Cyan("──────────────────────────────"))
		for _, t := range wave.Tasks {
			crit := " "
			if t.IsCritical {
				crit = ui.BoldYellow("⚡")
			}
			fmt.Printf("  %s [%s] %s\n", crit, ui.BoldMagenta(t.TaskID), t.Title)

			// Show edges
			for _, blocked := range g.Adj[t.TaskID] {
				fmt.Printf("      %s %s\n", ui.Dim("└──→"), ui.Magenta(blocked))
			}
		}
		fmt.Println()
	}
}

func printDOT(g *graph.TaskGraph, result *cpm.CPMResult) error {
	fmt.Println("digraph beadloom {")
	fmt.Println("  rankdir=LR;")
	fmt.Println("  node [shape=box, style=rounded];")
	fmt.Println()

	for id, task := range g.Tasks {
		label := fmt.Sprintf("%s\\n%s", id, task.Title)
		attrs := fmt.Sprintf(`label="%s"`, label)
		if schedule, ok := result.Tasks[id]; ok && schedule.IsCritical {
			attrs += `, style="rounded,bold", color=red`
		}
		fmt.Printf("  %q [%s];\n", id, attrs)
	}

	fmt.Println()

	for from, tos := range g.Adj {
		for _, to := range tos {
			style := ""
			if result.Tasks[from] != nil && result.Tasks[from].IsCritical &&
				result.Tasks[to] != nil && result.Tasks[to].IsCritical {
				style = ` [color=red, penwidth=2]`
			}
			fmt.Printf("  %q -> %q%s;\n", from, to, style)
		}
	}

	fmt.Println("}")
	return nil
}

// applyFilter parses simple filter expressions and returns a filtered graph.
func applyFilter(g *graph.TaskGraph, filter string) (*graph.TaskGraph, error) {
	// Supported formats: "priority<=N", "priority=N", "label=X", "type=X"
	if strings.HasPrefix(filter, "priority") {
		return filterByPriority(g, filter)
	}
	if strings.HasPrefix(filter, "label=") {
		label := strings.TrimPrefix(filter, "label=")
		return g.Filter(func(t *graph.Task) bool {
			for _, l := range t.Labels {
				if l == label {
					return true
				}
			}
			return false
		})
	}
	if strings.HasPrefix(filter, "type=") {
		typ := strings.TrimPrefix(filter, "type=")
		return g.Filter(func(t *graph.Task) bool {
			return t.Type == typ
		})
	}
	return nil, fmt.Errorf("unsupported filter: %s (use priority<=N, label=X, or type=X)", filter)
}

func filterByPriority(g *graph.TaskGraph, filter string) (*graph.TaskGraph, error) {
	filter = strings.TrimPrefix(filter, "priority")
	if strings.HasPrefix(filter, "<=") {
		n, err := strconv.Atoi(strings.TrimPrefix(filter, "<="))
		if err != nil {
			return nil, fmt.Errorf("invalid priority value: %w", err)
		}
		return g.Filter(func(t *graph.Task) bool { return t.Priority <= n })
	}
	if strings.HasPrefix(filter, "=") {
		n, err := strconv.Atoi(strings.TrimPrefix(filter, "="))
		if err != nil {
			return nil, fmt.Errorf("invalid priority value: %w", err)
		}
		return g.Filter(func(t *graph.Task) bool { return t.Priority == n })
	}
	if strings.HasPrefix(filter, ">=") {
		n, err := strconv.Atoi(strings.TrimPrefix(filter, ">="))
		if err != nil {
			return nil, fmt.Errorf("invalid priority value: %w", err)
		}
		return g.Filter(func(t *graph.Task) bool { return t.Priority >= n })
	}
	return nil, fmt.Errorf("unsupported priority filter: priority%s", filter)
}

func inferDepsCmd() *cobra.Command {
	var (
		flagApply    bool
		flagModel    string
		flagOutput   string
		flagFromFile string
	)

	cmd := &cobra.Command{
		Use:   "infer-deps",
		Short: "Use Claude to infer task dependencies from titles",
		Long: `Sends open task titles to Claude and infers dependency edges.
By default runs in dry-run mode — use --apply to write deps to beads.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			client := bd.NewClient("", flagDB)

			tasks, err := client.ListOpen()
			if err != nil {
				return fmt.Errorf("list open tasks: %w", err)
			}
			if len(tasks) == 0 {
				return fmt.Errorf("no open tasks found")
			}

			// Build task summaries for Claude
			summaries := make([]claude.TaskSummary, len(tasks))
			taskIDs := make(map[string]bool, len(tasks))
			for i, t := range tasks {
				summaries[i] = claude.TaskSummary{
					ID:       t.ID,
					Title:    t.Title,
					Priority: t.Priority,
					Type:     t.Type,
				}
				taskIDs[t.ID] = true
			}

			var result *claude.InferDepsResult
			if flagFromFile != "" {
				data, err := os.ReadFile(flagFromFile)
				if err != nil {
					return fmt.Errorf("read from-file: %w", err)
				}
				result = &claude.InferDepsResult{}
				if err := json.Unmarshal(data, result); err != nil {
					return fmt.Errorf("parse from-file: %w", err)
				}
				fmt.Printf("📂 Loaded %s edges from %s\n", ui.Bold(len(result.Edges)), ui.Dim(flagFromFile))
			} else {
				fmt.Printf("🔍 Sending %s tasks to Claude for dependency inference...\n", ui.Bold(len(summaries)))

				claudeClient, err := claude.NewClient("", flagModel)
				if err != nil {
					return err
				}

				ctx := context.Background()
				inferResult, err := claudeClient.InferDeps(ctx, summaries)
				if err != nil {
					return fmt.Errorf("infer deps: %w", err)
				}
				result = inferResult
			}

			// Validate edges: filter out unknown IDs and self-deps
			var valid []claude.DepEdge
			for _, e := range result.Edges {
				if !taskIDs[e.BlockedID] {
					fmt.Printf("  %s unknown blocked_id %s\n", ui.Yellow("⏭️  SKIP:"), e.BlockedID)
					continue
				}
				if !taskIDs[e.BlockerID] {
					fmt.Printf("  %s unknown blocker_id %s\n", ui.Yellow("⏭️  SKIP:"), e.BlockerID)
					continue
				}
				if e.BlockedID == e.BlockerID {
					fmt.Printf("  %s self-dep %s\n", ui.Yellow("⏭️  SKIP:"), e.BlockedID)
					continue
				}
				valid = append(valid, e)
			}

			// Cycle detection: greedily add edges, skip any that would create a cycle
			adj := make(map[string][]string)
			var accepted []claude.DepEdge
			for _, e := range valid {
				// Tentatively add edge: blocker -> blocked (blocker blocks blocked)
				adj[e.BlockerID] = append(adj[e.BlockerID], e.BlockedID)
				if hasCycleDFS(adj, taskIDs) {
					// Remove the edge
					adj[e.BlockerID] = adj[e.BlockerID][:len(adj[e.BlockerID])-1]
					fmt.Printf("  %s would create cycle: %s -> %s\n", ui.Yellow("⏭️  SKIP:"), e.BlockerID, e.BlockedID)
					continue
				}
				accepted = append(accepted, e)
			}

			if flagJSON {
				out := struct {
					Edges   []claude.DepEdge `json:"edges"`
					Summary string           `json:"summary"`
				}{
					Edges:   accepted,
					Summary: result.Summary,
				}
				if flagOutput != "" {
					data, err := json.MarshalIndent(out, "", "  ")
					if err != nil {
						return err
					}
					if err := os.WriteFile(flagOutput, data, 0644); err != nil {
						return err
					}
					fmt.Printf("Wrote %d edges to %s\n", len(accepted), flagOutput)
					return nil
				}
				return outputJSON(out)
			}

			fmt.Printf("\n🔗 Inferred %s dependencies (%d from Claude, %d after validation):\n\n",
				ui.Bold(len(accepted)), len(result.Edges), len(accepted))
			for _, e := range accepted {
				fmt.Printf("  %s %s blocked by %s  — %s\n", ui.Cyan("→"), ui.BoldMagenta(e.BlockedID), ui.BoldMagenta(e.BlockerID), ui.Dim(e.Reason))
			}
			if result.Summary != "" {
				fmt.Printf("\n💡 %s %s\n", ui.BoldWhite("Summary:"), result.Summary)
			}

			if !flagApply {
				fmt.Printf("\n🎯 %s\n", ui.Yellow("Dry run — use --apply to write these dependencies to beads."))
				return nil
			}

			fmt.Printf("\n📝 Applying %s dependencies...\n", ui.Bold(len(accepted)))
			applied := 0
			for _, e := range accepted {
				if err := client.AddDep(e.BlockedID, e.BlockerID); err != nil {
					fmt.Printf("  %s dep add %s %s: %v\n", ui.Red("❌ ERROR:"), e.BlockedID, e.BlockerID, err)
					continue
				}
				applied++
				fmt.Printf("  %s %s blocked by %s\n", ui.Green("✅ OK:"), ui.BoldMagenta(e.BlockedID), ui.BoldMagenta(e.BlockerID))
			}
			fmt.Printf("\n🏁 Applied %s/%d dependencies.\n", ui.BoldGreen(applied), len(accepted))
			return nil
		},
	}

	cmd.Flags().BoolVar(&flagApply, "apply", false, "Write inferred deps to beads (default: dry-run)")
	cmd.Flags().StringVar(&flagModel, "model", "", "Claude model to use (default: Sonnet)")
	cmd.Flags().StringVarP(&flagOutput, "output", "o", "", "Save JSON output to file (use with --json)")
	cmd.Flags().StringVar(&flagFromFile, "from-file", "", "Load inferred deps from a JSON file instead of calling Claude")

	return cmd
}

// hasCycleDFS checks if the adjacency list contains any cycle using DFS coloring.
func hasCycleDFS(adj map[string][]string, nodeSet map[string]bool) bool {
	const (
		white = 0
		gray  = 1
		black = 2
	)

	color := make(map[string]int)

	var dfs func(node string) bool
	dfs = func(node string) bool {
		color[node] = gray
		for _, next := range adj[node] {
			if color[next] == gray {
				return true
			}
			if color[next] == white {
				if dfs(next) {
					return true
				}
			}
		}
		color[node] = black
		return false
	}

	for id := range nodeSet {
		if color[id] == white {
			if dfs(id) {
				return true
			}
		}
	}
	return false
}
