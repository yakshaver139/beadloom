package ui

import (
	"fmt"
	"io"
	"sync"

	"github.com/tidwall/gjson"
)

// StreamFormatter parses Claude stream-json output and writes
// human-readable lines to dest. It implements io.Writer.
type StreamFormatter struct {
	prefix string
	dest   io.Writer
	mu     *sync.Mutex
	buf    []byte
}

// NewStreamFormatter creates a StreamFormatter that prefixes output with [taskID].
func NewStreamFormatter(taskID string, dest io.Writer, mu *sync.Mutex) *StreamFormatter {
	return &StreamFormatter{
		prefix: TaskPrefix(taskID) + " ",
		dest:   dest,
		mu:     mu,
	}
}

func (sf *StreamFormatter) Write(p []byte) (int, error) {
	sf.mu.Lock()
	defer sf.mu.Unlock()

	sf.buf = append(sf.buf, p...)
	for {
		idx := -1
		for i, b := range sf.buf {
			if b == '\n' {
				idx = i
				break
			}
		}
		if idx == -1 {
			break
		}
		line := string(sf.buf[:idx])
		sf.buf = sf.buf[idx+1:]
		sf.processLine(line)
	}
	return len(p), nil
}

func (sf *StreamFormatter) processLine(line string) {
	if !gjson.Valid(line) {
		return
	}

	eventType := gjson.Get(line, "type").String()

	switch eventType {
	case "assistant":
		sf.processAssistant(line)
	}
	// Skip "user" (tool_result), "system", and everything else
}

func (sf *StreamFormatter) processAssistant(line string) {
	content := gjson.Get(line, "message.content")
	if !content.Exists() {
		return
	}

	content.ForEach(func(_, item gjson.Result) bool {
		contentType := item.Get("type").String()
		switch contentType {
		case "text":
			text := item.Get("text").String()
			if text != "" {
				sf.writeLine(fmt.Sprintf("💬 %s", text))
			}
		case "tool_use":
			sf.processToolUse(item)
		}
		// Skip "thinking" — too verbose
		return true
	})
}

func (sf *StreamFormatter) processToolUse(item gjson.Result) {
	name := item.Get("name").String()
	input := item.Get("input")

	var display string
	switch name {
	case "Bash":
		desc := input.Get("description").String()
		if desc != "" {
			display = "🔧 $ " + desc
		} else {
			cmd := input.Get("command").String()
			if len(cmd) > 80 {
				cmd = cmd[:80] + "..."
			}
			display = "🔧 $ " + cmd
		}
	case "Read":
		display = "📖 Reading " + input.Get("file_path").String()
	case "Write":
		display = "✏️  Writing " + input.Get("file_path").String()
	case "Edit":
		display = "✏️  Editing " + input.Get("file_path").String()
	case "Glob":
		display = "🔍 Searching " + input.Get("pattern").String()
	case "Grep":
		display = "🔍 Grepping " + input.Get("pattern").String()
	case "Task":
		desc := input.Get("description").String()
		if desc != "" {
			display = "🤖 Spawning agent: " + desc
		} else {
			display = "🤖 Spawning agent"
		}
	default:
		display = "🔧 " + name
	}

	sf.writeLine(Dim(display))
}

func (sf *StreamFormatter) writeLine(text string) {
	fmt.Fprintf(sf.dest, "  %s%s\n", sf.prefix, text)
}
