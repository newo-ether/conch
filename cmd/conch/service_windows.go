//go:build windows

package main

import (
	"log"

	"golang.org/x/sys/windows/svc"
)

type conchService struct {
	run  func() error
	stop func()
}

func (s *conchService) Execute(args []string, r <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const cmdsAccepted = svc.AcceptStop | svc.AcceptShutdown

	status <- svc.Status{State: svc.StartPending}

	errCh := make(chan error, 1)
	go func() {
		errCh <- s.run()
	}()

	status <- svc.Status{State: svc.Running, Accepts: cmdsAccepted}

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				log.Println("service stop requested, shutting down...")
				s.stop()
				return false, 0
			default:
				log.Printf("unexpected service control: %v", c.Cmd)
			}
		case err := <-errCh:
			if err != nil {
				log.Printf("server error: %v", err)
				return false, 1
			}
			return false, 0
		}
	}
}

// runService enters the SCM loop if running as a Windows service.
// Returns true if running as a service (blocks), false otherwise.
func runService(name string, run func() error, stop func()) bool {
	inService, err := svc.IsWindowsService()
	if err != nil {
		log.Fatalf("failed to check service state: %v", err)
	}
	if !inService {
		return false
	}
	svc.Run(name, &conchService{run: run, stop: stop})
	return true
}
