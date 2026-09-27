package async

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// The package logger in place before SetLogger is a contract.Logger that
// writes every level through the standard library log package; Fatal
// writes at error level and returns.
func TestStdLogger_WritesEveryLevelThroughStdlibLog(t *testing.T) {
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	var l contract.Logger = stdLogger{}
	l.Debug("debug line", "k", 1)
	l.Info("info line")
	l.Warn("warn line")
	l.Error("error line")
	l.Fatal("fatal line")

	for _, want := range []string{
		"[DEBUG] debug line k=1\n",
		"[INFO] info line\n",
		"[WARN] warn line\n",
		"[ERROR] error line\n",
		"[ERROR] fatal line\n",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("stdlib log output %q lacks %q", buf.String(), want)
		}
	}
}
