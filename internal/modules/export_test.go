package modules

import "time"

// Hooks for the tests in package modules_test.

func WaitGbanTasks() { gbanTasks.Wait() }

func WaitFedTasks() { fedTasks.Wait() }

func init() {
	unbanPause = 0
	sleepFor = func(time.Duration) {}
}

func WaitZombieTasks() { zombieTasks.Wait() }

func init() { zombiePause = 0 }

func ForgetRecent(chat, user int64) { forgetRecent(chat, user) }
