package allocation

import (
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pion/logging"
	"github.com/pion/transport/vnet"
	"github.com/pkg/errors"
)

// ManagerConfig a bag of config params for Manager.
type ManagerConfig struct {
	LeveledLogger logging.LeveledLogger
	Net           *vnet.Net
}

// Manager is used to hold active allocations
type Manager struct {
	lock        sync.RWMutex
	allocations []*Allocation
	log         logging.LeveledLogger
	net         *vnet.Net
}

// NewManager creates a new instance of Manager.
func NewManager(config *ManagerConfig) *Manager {
	if config.Net == nil {
		config.Net = vnet.NewNet(nil) // defaults to native operation
	}
	return &Manager{
		log: config.LeveledLogger,
		net: config.Net,
	}
}

// GetAllocation fetches the allocation matching the passed FiveTuple
func (m *Manager) GetAllocation(fiveTuple *FiveTuple) *Allocation {
	m.lock.Lock()
	defer m.lock.Unlock()
	for _, a := range m.allocations {
		if a.fiveTuple.Equal(fiveTuple) {
			return a
		}
	}
	return nil
}

// Close closes the manager and closes all allocations it manages
func (m *Manager) Close() error {
	m.lock.Lock()
	defer m.lock.Unlock()

	for _, a := range m.allocations {
		if err := a.Close(); err != nil {
			return err
		}

	}
	return nil
}

// CreateAllocation creates a new allocation and starts relaying
func (m *Manager) CreateAllocation(fiveTuple *FiveTuple, turnSocket net.PacketConn, requestedPort int, lifetime time.Duration) (*Allocation, error) {
	if fiveTuple == nil {
		return nil, errors.Errorf("Allocations must not be created with nil FivTuple")
	}
	if fiveTuple.SrcAddr == nil {
		return nil, errors.Errorf("Allocations must not be created with nil FiveTuple.SrcAddr")
	}
	if fiveTuple.DstAddr == nil {
		return nil, errors.Errorf("Allocations must not be created with nil FiveTuple.DstAddr")
	}
	if a := m.GetAllocation(fiveTuple); a != nil {
		return nil, errors.Errorf("Allocation attempt created with duplicate FiveTuple %v", fiveTuple)
	}
	if turnSocket == nil {
		return nil, errors.Errorf("Allocations must not be created with nil turnSocket")
	}
	if lifetime == 0 {
		return nil, errors.Errorf("Allocations must not be created with a lifetime of 0")
	}

	a := &Allocation{
		fiveTuple:  fiveTuple,
		TurnSocket: turnSocket,
		closed:     make(chan interface{}),
		log:        m.log,
	}

	network := "udp4"
	conn, err := m.net.ListenPacket(network, fmt.Sprintf("0.0.0.0:%d", requestedPort))
	if err != nil {
		return nil, err
	}

	a.RelaySocket = conn
	a.RelayAddr = conn.LocalAddr()

	a.lifetimeTimer = time.AfterFunc(lifetime, func() {
		if err := conn.Close(); err != nil {
			a.log.Errorf("Failed to close listener for %v", a.fiveTuple)
		}
	})

	m.lock.Lock()
	m.allocations = append(m.allocations, a)
	m.lock.Unlock()

	go a.packetHandler(m)
	return a, nil
}

// DeleteAllocation removes an allocation
func (m *Manager) DeleteAllocation(fiveTuple *FiveTuple) bool {
	m.lock.Lock()
	defer m.lock.Unlock()

	for i := len(m.allocations) - 1; i >= 0; i-- {
		allocation := m.allocations[i]
		if allocation.fiveTuple.Equal(fiveTuple) {
			if err := allocation.Close(); err != nil {
				m.log.Errorf("Failed to close allocation: %v", err)
			}
			m.allocations = append(m.allocations[:i], m.allocations[i+1:]...)
			return true
		}
	}

	return false
}
