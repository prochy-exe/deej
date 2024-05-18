package deej

import (
	"fmt"
	"strings"

	"go.uber.org/zap"
)

// Session represents a single addressable audio session
type Session interface {
	GetVolume() float32
	SetVolume(v float32) error

	// TODO: future mute support
	// GetMute() bool
	// SetMute(m bool) error

	Key() string
	Release()
}

const (

	// ideally these would share a common ground in baseSession
	// but it will not call the child GetVolume correctly :/
	sessionCreationLogMessage = "Created audio session instance"

	// format this with s.humanReadableDesc and whatever the current volume is
	sessionStringFormat = "<session: %s, vol: %.2f>"
)

type baseSession struct {
	logger *zap.SugaredLogger
	system bool
	master bool

	// used by Key(), needs to be set by child
	name string

	// used by String(), needs to be set by child
	humanReadableDesc string
}

type controlifySession struct {
	baseSession

	volume       float32
	deejInstance *Deej
}

func (s *baseSession) Key() string {
	if s.system {
		return systemSessionName
	}

	if s.master {
		return strings.ToLower(s.name)
	}

	return strings.ToLower(s.name)
}

func newControlifySession(
	logger *zap.SugaredLogger,
	volume float32,
	key string,
	loggerKey string,
) (*controlifySession, error) {

	s := &controlifySession{
		volume:       volume,
		deejInstance: &Deej{},
	}

	s.logger = logger.Named(loggerKey)
	s.name = key
	s.humanReadableDesc = key

	s.logger.Debugw(sessionCreationLogMessage, "session", s)
	go s.deejInstance.connectToControlify(s.logger)

	return s, nil
}

func (s *controlifySession) GetVolume() float32 {
	return s.volume
}

func (s *controlifySession) SetVolume(v float32) error {
	s.volume = v
	s.logger.Debugw("Adjusting session volume", "to", fmt.Sprintf("%.2f", v))
	s.deejInstance.SendVolume(s.Key(), v)
	return nil
}

func (s *controlifySession) Key() string {
	return s.name
}

func (s *controlifySession) Release() {
	s.logger.Debug("Releasing Controlify session")
	s.deejInstance.disconnectSocket(s.logger)
}

func (s *controlifySession) String() string {
	return fmt.Sprintf(sessionStringFormat, s.humanReadableDesc, s.GetVolume())
}
