package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestQuickTerminalFixture(t *testing.T) {
	if os.Getenv("BP_TUI_PTY_FIXTURE") == "" {
		t.Skip("launched by the PTY test")
	}
	defer QuickDefaults()()
	fmt.Println("FIRST=" + PromptDefault("first", "kept"))
	fmt.Println("SECOND=" + PromptDefault("second", "old"))
	if ChooseOptDefault("pick", []Option{{Title: "one"}, {Title: "two"}}, 1) != 1 {
		t.Fatal("space chose wrong menu default")
	}
	if Confirm("create", true) {
		t.Fatal("typed refusal ignored")
	}
}

func TestQuickSpaceWorksWithoutEnterAndRestoresTerminal(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 needed for PTY fixture")
	}
	self, _ := os.Executable()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-c", `
import os,pty,termios,subprocess,select,time,sys
master,slave=pty.openpty()
original=termios.tcgetattr(slave)
env=dict(os.environ,BP_TUI_PTY_FIXTURE='1')
child=subprocess.Popen([sys.argv[1],'-test.run','^TestQuickTerminalFixture$','-test.v'],stdin=slave,stdout=slave,stderr=slave,env=env)
out=b''
def until(text):
 global out
 end=time.monotonic()+5
 while text not in out:
  assert time.monotonic()<end,(text,out)
  if select.select([master],[],[],.1)[0]:out+=os.read(master,65536)
try:
 until(b'first')
 os.write(master,b' ')
 until(b'FIRST=kept') # Space alone must complete the prompt.
 until(b'second')
 os.write(master,b'changedX\x7f\n')
 until(b'SECOND=changed')
 until(b'Choice [2]')
 os.write(master,b' ')
 until(b'create')
 os.write(master,b'n\n')
 assert child.wait(timeout=5)==0,out
 assert termios.tcgetattr(slave)==original,'terminal mode leaked'
 # Cancelling while a quick prompt is active must restore the terminal too.
 out=b''
 child=subprocess.Popen([sys.argv[1],'-test.run','^TestQuickTerminalFixture$','-test.v'],stdin=slave,stdout=slave,stderr=slave,env=env)
 until(b'first')
 os.write(master,b'\x03')
 assert child.wait(timeout=5)==0
 assert termios.tcgetattr(slave)==original,'cancel leaked terminal mode'
finally:
 if child.poll() is None:child.kill();child.wait()
 os.close(master);os.close(slave)
`, self)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
