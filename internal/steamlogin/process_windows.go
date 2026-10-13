package steamlogin

import (
	"os/exec"
	"time"
)

func configureProcess(command *exec.Cmd) { command.WaitDelay = time.Second }
