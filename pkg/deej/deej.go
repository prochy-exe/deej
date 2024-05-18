// Package deej provides a machine-side client that pairs with an Arduino
// chip to form a tactile, physical volume control system.
package deej

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/gorilla/websocket"
	"github.com/omriharel/deej/pkg/deej/util"
)

const (
	// when this is set to anything, deej won't use a tray icon
	envNoTray = "DEEJ_NO_TRAY_ICON"
)

var (
	socket        *websocket.Conn
	mu            sync.Mutex
	hasControlify bool
)

// Deej is the main entity managing access to all sub-components
type Deej struct {
	logger   *zap.SugaredLogger
	notifier Notifier
	config   *CanonicalConfig
	serial   *SerialIO
	sessions *sessionMap

	stopChannel chan bool
	version     string
	verbose     bool
}

// NewDeej creates a Deej instance
func NewDeej(logger *zap.SugaredLogger, verbose bool) (*Deej, error) {
	logger = logger.Named("deej")

	notifier, err := NewToastNotifier(logger)
	if err != nil {
		logger.Errorw("Failed to create ToastNotifier", "error", err)
		return nil, fmt.Errorf("create new ToastNotifier: %w", err)
	}

	config, err := NewConfig(logger, notifier)
	if err != nil {
		logger.Errorw("Failed to create Config", "error", err)
		return nil, fmt.Errorf("create new Config: %w", err)
	}

	d := &Deej{
		logger:      logger,
		notifier:    notifier,
		config:      config,
		stopChannel: make(chan bool),
		verbose:     verbose,
	}

	serial, err := NewSerialIO(d, logger)
	if err != nil {
		logger.Errorw("Failed to create SerialIO", "error", err)
		return nil, fmt.Errorf("create new SerialIO: %w", err)
	}

	d.serial = serial

	sessionFinder, err := newSessionFinder(logger)
	if err != nil {
		logger.Errorw("Failed to create SessionFinder", "error", err)
		return nil, fmt.Errorf("create new SessionFinder: %w", err)
	}

	sessions, err := newSessionMap(d, logger, sessionFinder)
	if err != nil {
		logger.Errorw("Failed to create sessionMap", "error", err)
		return nil, fmt.Errorf("create new sessionMap: %w", err)
	}

	d.sessions = sessions

	logger.Debug("Created deej instance")

	return d, nil
}

// Initialize sets up components and starts to run in the background
func (d *Deej) Initialize() error {
	d.logger.Debug("Initializing")

	// load the config for the first time
	if err := d.config.Load(); err != nil {
		d.logger.Errorw("Failed to load config during initialization", "error", err)
		return fmt.Errorf("load config during init: %w", err)
	}

	// initialize the session map
	if err := d.sessions.initialize(); err != nil {
		d.logger.Errorw("Failed to initialize session map", "error", err)
		return fmt.Errorf("init session map: %w", err)
	}

	if d.sessions.Contains("controlify") {
		hasControlify = true
	}

	// decide whether to run with/without tray
	if _, noTraySet := os.LookupEnv(envNoTray); noTraySet {

		d.logger.Debugw("Running without tray icon", "reason", "envvar set")

		// run in main thread while waiting on ctrl+C
		d.setupInterruptHandler()
		d.run()

	} else {
		d.setupInterruptHandler()
		d.initializeTray(d.run)
	}

	return nil
}

func (d *Deej) connectToControlify(logger *zap.SugaredLogger) {
	if hasControlify {
		go d.connectToWebSocketServer(logger)
	}
}

func (d *Deej) connectToWebSocketServer(logger *zap.SugaredLogger) {
	var retryCount int
	var retryMaxCount = 5
	var retryTimeout = 15 * time.Second

	for {
		if socket != nil {
			break
		}
		mu.Lock()
		var err error
		socket, _, err = websocket.DefaultDialer.Dial("ws://localhost:8999", nil)
		mu.Unlock()
		if err != nil {
			socket = nil
			if retryCount < retryMaxCount || retryMaxCount == 0 {
				retryCount++
				logger.Warnw("Failed to connect to WebSocket server, retrying...", "error", err, "retries", retryCount)
				time.Sleep(retryTimeout)
				continue
			} else {
				logger.Errorw("Failed to connect to WebSocket server after multiple attempts, giving up.", "error", err)
				return
			}
		}

		// Send initial message with the identifier
		initialMessage := map[string]interface{}{"clientID": "deej-client"}
		err = socket.WriteJSON(initialMessage)
		if err != nil {
			logger.Errorw("Failed to send initial message", "error", err)
			socket.Close()
			socket = nil
			continue
		}

		retryCount = 0
		logger.Debug("WebSocket connection established")

		// Handle heartbeats and messages
		go d.handleWebSocketMessages(logger)

		break
	}
}

func (d *Deej) handleWebSocketMessages(logger *zap.SugaredLogger) {
	for {
		_, _, err := socket.ReadMessage()
		if err != nil {
			logger.Errorw("WebSocket read error", "error", err)
			socket.Close()
			socket = nil
			go d.connectToWebSocketServer(logger)
			break
		}
	}
}

func (d *Deej) SendVolume(sessionKey string, v float32) {
	if socket != nil {
		msg := map[string]interface{}{"setVolume": v}
		if err := socket.WriteJSON(msg); err != nil {
			d.logger.Errorw("Failed to send volume message", "error", err)
		}
	}
}

// Contains checks if the config's slider mapping contains a session with the given name
func (sm *sessionMap) Contains(name string) bool {
	sm.lock.Lock()
	defer sm.lock.Unlock()
	sm.logger.Debugw("Checking if config slider mapping contains session", "sessionName", name)
	sm.logger.Debugw("Config slider mapping", "sliderMapping", sm.deej.config.SliderMapping.m)

	// Check the slider mapping in the config
	for _, mapped := range sm.deej.config.SliderMapping.m {
		for _, appName := range mapped {
			if strings.EqualFold(appName, name) {
				return true
			}
		}
	}
	return false
}

func (d *Deej) disconnectSocket(logger *zap.SugaredLogger) {
	if socket != nil {
		logger.Debug("Closing WebSocket connection")
		socket.Close()
	}
}

// SetVersion causes deej to add a version string to its tray menu if called before Initialize
func (d *Deej) SetVersion(version string) {
	d.version = version
}

// Verbose returns a boolean indicating whether deej is running in verbose mode
func (d *Deej) Verbose() bool {
	return d.verbose
}

func (d *Deej) setupInterruptHandler() {
	interruptChannel := util.SetupCloseHandler()

	if d.sessions.Contains("controlify") {
		// Connect to the WebSocket server
		go d.connectToWebSocketServer(d.logger)
	}

	go func() {
		signal := <-interruptChannel
		d.logger.Debugw("Interrupted", "signal", signal)
		d.signalStop()
	}()
}

func (d *Deej) run() {
	d.logger.Info("Run loop starting")

	// watch the config file for changes
	go d.config.WatchConfigFileChanges()

	// connect to the arduino for the first time
	go func() {
		if err := d.serial.Start(); err != nil {
			d.logger.Warnw("Failed to start first-time serial connection", "error", err)

			// If the port is busy, that's because something else is connected - notify and quit
			if errors.Is(err, os.ErrPermission) {
				d.logger.Warnw("Serial port seems busy, notifying user and closing",
					"comPort", d.config.ConnectionInfo.COMPort)

				d.notifier.Notify(fmt.Sprintf("Can't connect to %s!", d.config.ConnectionInfo.COMPort),
					"This serial port is busy, make sure to close any serial monitor or other deej instance.")

				d.signalStop()

				// also notify if the COM port they gave isn't found, maybe their config is wrong
			} else if errors.Is(err, os.ErrNotExist) {
				d.logger.Warnw("Provided COM port seems wrong, notifying user and closing",
					"comPort", d.config.ConnectionInfo.COMPort)

				d.notifier.Notify(fmt.Sprintf("Can't connect to %s!", d.config.ConnectionInfo.COMPort),
					"This serial port doesn't exist, check your configuration and make sure it's set correctly.")

				d.signalStop()
			}
		}
	}()

	// wait until stopped (gracefully)
	<-d.stopChannel
	d.logger.Debug("Stop channel signaled, terminating")

	if err := d.stop(); err != nil {
		d.logger.Warnw("Failed to stop deej", "error", err)
		os.Exit(1)
	} else {
		// exit with 0
		os.Exit(0)
	}
}

func (d *Deej) signalStop() {
	d.logger.Debug("Signalling stop channel")
	d.stopChannel <- true
}

func (d *Deej) stop() error {
	d.logger.Info("Stopping")

	d.config.StopWatchingConfigFile()
	d.serial.Stop()

	// release the session map
	if err := d.sessions.release(); err != nil {
		d.logger.Errorw("Failed to release session map", "error", err)
		return fmt.Errorf("release session map: %w", err)
	}

	d.stopTray()

	// attempt to sync on exit - this won't necessarily work but can't harm
	d.logger.Sync()

	return nil
}
