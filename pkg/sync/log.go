/*
Copyright 2026 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package sync

import (
	"fmt"
	"io"
	"strings"
)

// Glyph marks what a log line reports.
type Glyph string

const (
	// GlyphCreate marks a resource that is created.
	GlyphCreate Glyph = "+"

	// GlyphDelete marks a resource that is deleted.
	GlyphDelete Glyph = "-"

	// GlyphUpdate marks a resource whose meta is rewritten.
	GlyphUpdate Glyph = "✎"

	// GlyphReport marks a resource that needs attention but is not touched,
	// such as an unmanaged package under the report policy.
	GlyphReport Glyph = "!"
)

// indentWidth is the number of spaces a nesting level adds.
const indentWidth = 2

// Logger writes the human readable change log. The very same lines are
// produced in a dry run and in a real run, so that the dry run output is an
// exact proposal of what a real run does.
type Logger struct {
	out io.Writer
}

// NewLogger creates a logger writing to out.
func NewLogger(out io.Writer) *Logger {
	return &Logger{out: out}
}

// Change reports an action on a resource. The level nests the line, with the
// project at level zero and its packages at level one.
func (l *Logger) Change(level int, glyph Glyph, kind, name string) error {
	return l.printf("%s%s %s %s", indent(level), glyph, kind, name)
}

// Detailf reports a single field level change below a Change line.
func (l *Logger) Detailf(level int, format string, args ...any) error {
	return l.printf("%s%s", indent(level+1)+"  ", fmt.Sprintf(format, args...))
}

// Details reports every entry of a change list below a Change line.
func (l *Logger) Details(level int, changes []string) error {
	for _, change := range changes {
		if err := l.Detailf(level, "%s", change); err != nil {
			return err
		}
	}

	return nil
}

// Infof reports a line that is not tied to a resource.
func (l *Logger) Infof(format string, args ...any) error {
	return l.printf(format, args...)
}

// printf writes a single line.
func (l *Logger) printf(format string, args ...any) error {
	if l.out == nil {
		return nil
	}

	if _, err := fmt.Fprintf(l.out, format+"\n", args...); err != nil {
		return fmt.Errorf("writing log output: %w", err)
	}

	return nil
}

// indent returns the padding of a nesting level.
func indent(level int) string {
	if level <= 0 {
		return ""
	}

	return strings.Repeat(" ", level*indentWidth)
}
