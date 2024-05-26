package deej

import (
	"time"

	"fyne.io/systray"

	"github.com/omriharel/deej/pkg/deej/icon"
	"github.com/omriharel/deej/pkg/deej/util"
)

func (d *Deej) initializeTray(onDone func()) {
	logger := d.logger.Named("tray")

	var socketStatus string

	onReady := func() {
		logger.Debug("Tray instance ready")

		systray.SetTemplateIcon(icon.DeejLogo, icon.DeejLogo)
		systray.SetTitle("deej")
		systray.SetTooltip("deej")

		editConfig := systray.AddMenuItem("Edit configuration", "Open config file with notepad")
		editConfig.SetIcon(icon.EditConfig)

		refreshSessions := systray.AddMenuItem("Re-scan audio sessions", "Manually refresh audio sessions if something's stuck")
		refreshSessions.SetIcon(icon.RefreshSessions)

		controlifyRetry := systray.AddMenuItem("Connecting to Controlify...", "Connecting to Controlify...")
		controlifyRetry.Disable()

		if d.version != "" {
			systray.AddSeparator()
			versionInfo := systray.AddMenuItem(d.version, "")
			versionInfo.Disable()
		}

		systray.AddSeparator()
		quit := systray.AddMenuItem("Quit", "Stop deej and quit")

		// Listen for controlify connection status
		go func() {
			for {
				if hasControlify {
					mu.Lock()
					if controlifyClosed && socketStatus != "closed" {
						socketStatus = "closed"
						logger.Debug("Updating tray, Controlify connection closed")
						controlifyRetry.Enable()
						controlifyRetry.SetTitle("Retry connecting to Controlify")
						controlifyRetry.SetTooltip("Retry connecting to Controlify")
					} else if !controlifyClosed && socket != nil && socketStatus != "open" {
						socketStatus = "open"
						logger.Debug("Updating tray, Controlify connection active")
						controlifyRetry.Disable()
						controlifyRetry.SetTitle("Controlify connection active")
						controlifyRetry.SetTooltip("Connected to Controlify")
					} else if retryCount > 0 && socketStatus != "retrying" {
						socketStatus = "retrying"
						logger.Debug("Updating tray, Controlify connection retrying")
						controlifyRetry.SetTitle("Connecting to Controlify...")
						controlifyRetry.SetTooltip("Connecting to Controlify...")
						controlifyRetry.Disable()
					}
					mu.Unlock()
				} else {
					logger.Debug("controlify support disabled, hiding controlify tray menu")
					mu.Lock()
					controlifyRetry.Hide()
					mu.Unlock()
				}
				time.Sleep(3 * time.Second)
			}
		}()

		// wait on things to happen
		go func() {
			for {
				select {

				// quit
				case <-quit.ClickedCh:
					logger.Info("Quit menu item clicked, stopping")

					d.signalStop()

				// edit config
				case <-editConfig.ClickedCh:
					logger.Info("Edit config menu item clicked, opening config for editing")

					editor := "notepad.exe"
					if util.Linux() {
						editor = "gedit"
					}

					if err := util.OpenExternal(logger, editor, userConfigFilepath); err != nil {
						logger.Warnw("Failed to open config file for editing", "error", err)
					}

				// refresh sessions
				case <-refreshSessions.ClickedCh:
					logger.Info("Refresh sessions menu item clicked, triggering session map refresh")

					// performance: the reason that forcing a refresh here is okay is that users can't spam the
					// right-click -> select-this-option sequence at a rate that's meaningful to performance
					d.sessions.refreshSessions(true)

				case <-controlifyRetry.ClickedCh:
					logger.Info("Retry Controlify menu item clicked, retrying connection")

					go d.connectToControlify(logger)
					socketStatus = "retrying"
					logger.Debug("Updating tray, Controlify connection retrying")
					controlifyRetry.SetTitle("Connecting to Controlify...")
					controlifyRetry.SetTooltip("Connecting to Controlify...")
					controlifyRetry.Disable()
				}
			}
		}()

		// actually start the main runtime
		onDone()
	}

	onExit := func() {
		logger.Debug("Tray exited")
	}

	// start the tray icon
	logger.Debug("Running in tray")
	systray.Run(onReady, onExit)
}

func (d *Deej) stopTray() {
	d.logger.Debug("Quitting tray")
	systray.Quit()
}
