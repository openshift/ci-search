package proc

import (
	"bufio"
	"io/ioutil"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"k8s.io/klog"
)

// parseProcForZombies parses the current procfs mounted at /proc
// to find proccess in the zombie state.
func parseProcForZombies() ([]int, error) {
	files, err := ioutil.ReadDir("/proc")
	if err != nil {
		return nil, err
	}

	var zombies []int
	for _, file := range files {
		processID, err := strconv.Atoi(file.Name())
		if err != nil {
			break
		}
		stateFilePath := filepath.Join("/proc", file.Name(), "status")
		fd, err := os.Open(stateFilePath)
		if err != nil {
			klog.V(4).Infof("Failed to open %q: %v", stateFilePath, err)
			continue
		}
		defer fd.Close()
		fs := bufio.NewScanner(fd)
		for fs.Scan() {
			line := fs.Text()
			if strings.HasPrefix(line, "State:") {
				if strings.Contains(line, "zombie") {
					zombies = append(zombies, processID)
				}
				break
			}
		}
	}

	return zombies, nil
}

// StartPeriodicReaper starts a goroutine to reap processes periodically if called
// from a pid 1 process.
// The zombie processes are reaped at the beginning of next cycle, so os.Exec calls
// have an oppurtunity to reap their children within `period` seconds.
func StartPeriodicReaper(period int64) {
	if os.Getpid() == 1 {
		klog.V(4).Infof("Launching periodic reaper")
		go func() {
			var zs []int
			var err error
			for {
				for _, z := range zs {
					klog.V(4).Infof("Found a zombie: %d", z)
					cpid, err := syscall.Wait4(z, nil, syscall.WNOHANG, nil)
					if err != nil {
						klog.V(4).Infof("Zombie reap error: %v", err)
					} else {
						klog.V(4).Infof("Zombie reaped: %d", cpid)
					}
				}
				zs, err = parseProcForZombies()
				if err != nil {
					klog.V(4).Infof(err.Error())
					continue
				}
				klog.V(4).Infof("Sleeping for %v seconds", period)
				time.Sleep(time.Duration(period) * time.Second)
			}
		}()
	}
}

// StartReaper starts a goroutine to reap processes if called from a process
// that has pid 1.
func StartReaper() {
	if os.Getpid() == 1 {
		klog.V(4).Infof("Launching reaper")
		go func() {
			sigs := make(chan os.Signal, 1)
			signal.Notify(sigs, syscall.SIGCHLD)
			for {
				// Wait for a child to terminate
				sig := <-sigs
				klog.V(4).Infof("Signal received: %v", sig)
				for {
					// Reap processes
					cpid, _ := syscall.Wait4(-1, nil, syscall.WNOHANG, nil)
					if cpid < 1 {
						break
					}

					klog.V(4).Infof("Reaped process with pid %d", cpid)
				}
			}
		}()
	}
}
