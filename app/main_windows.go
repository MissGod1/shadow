// +build windows

package app

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sys/windows"

	"github.com/imgk/shadow/device/windivert"
	"github.com/imgk/shadow/log"
	"github.com/imgk/shadow/netstack"
	"github.com/imgk/shadow/protocol"
	"github.com/imgk/shadow/utils"
)

func CloseMutex(mutex windows.Handle) {
	windows.ReleaseMutex(mutex)
	windows.CloseHandle(mutex)
}

func Run(mode bool, ctx context.Context, re chan struct{}) error {
	log.SetMode(mode)

	mutex, err := windows.OpenMutex(windows.MUTEX_ALL_ACCESS, false, windows.StringToUTF16Ptr("SHADOW-MUTEX"))
	if err == nil {
		windows.CloseHandle(mutex)
		return fmt.Errorf("shadow is already running")
	}
	mutex, err = windows.CreateMutex(nil, false, windows.StringToUTF16Ptr("SHADOW-MUTEX"))
	if err != nil {
		return fmt.Errorf("create mutex error: %w", err)
	}
	defer CloseMutex(mutex)

	event, err := windows.WaitForSingleObject(mutex, windows.INFINITE)
	if err != nil {
		return fmt.Errorf("wait for mutex error: %w", err)
	}
	switch event {
	case windows.WAIT_OBJECT_0, windows.WAIT_ABANDONED:
	default:
		return fmt.Errorf("wait for mutex event id error: %w", event)
	}

	plugin, err := LoadPlugin(conf.Plugin, conf.PluginOpts)
	if conf.Plugin != "" && err != nil {
		return fmt.Errorf("plugin %v error: %w", conf.Plugin, err)
	}

	if plugin != nil {
		if plugin.Start(); err != nil {
			return fmt.Errorf("plugin start error: %w", err)
		}
		defer plugin.Stop()
		log.Logf("plugin %v start", conf.Plugin)

		go func() {
			if err := plugin.Wait(); err != nil {
				log.Logf("plugin error: %v", err)
				return
			}
			log.Logf("plugin %v stop", conf.Plugin)
		}()
	}

	handler, err := protocol.NewHandler(conf.Server, time.Minute)
	if err != nil {
		return fmt.Errorf("shadowsocks error %w", err)
	}

	dev, err := windivert.NewDevice(conf.FilterString)
	if err != nil {
		return fmt.Errorf("windivert error: %w", err)
	}
	defer dev.Close()
	LoadAppRules(dev.AppFilter)
	LoadIPRules(dev.IPFilter)

	stack := netstack.NewStack(handler, dev)
	defer stack.Close()
	if err := stack.SetResolver(conf.NameServer); err != nil {
		return fmt.Errorf("dns server error")
	}
	LoadDomainRules(stack.Tree)

	go func() {
		if _, err := dev.WriteTo(stack); err != nil {
			log.Logf("netstack exit error: %v", err)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			break
		case <-re:
			LoadAppRules(dev.AppFilter)
			LoadIPRules(dev.IPFilter)
			LoadDomainRules(stack.Tree)
		}
	}

	return nil
}

func (p *Plugin) Stop() error {
	if err := p.Cmd.Process.Signal(windows.SIGTERM); err != nil {
		// return fmt.Errorf("signal plugin process error: %v", err) // windows is not supported
	}

	select {
	case <-p.closed:
		return nil
	case <-time.After(time.Second):
		if err := p.Cmd.Process.Kill(); err != nil {
			return fmt.Errorf("kill plugin process error: %v", err)
		}
		p.closed <- struct{}{}
	}

	return nil
}

func LoadAppRules(appfilter *utils.AppFilter) {
	appfilter.Lock()
	defer appfilter.Unlock()

	appfilter.UnsafeReset()
	appfilter.UnsafeSetMode(conf.AppRules.Mode)

	for _, v := range conf.AppRules.Programs {
		appfilter.UnsafeAdd(v)
	}
}
