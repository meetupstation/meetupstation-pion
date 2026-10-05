package main

import (
	"fmt"
	"os"
	"sync"

	"github.com/pion/webrtc/v4"
)

type Peer struct {
	peerConnectionMutex sync.RWMutex
	peerConnection      *webrtc.PeerConnection
	connectionId        uint64
	localVideoTrack     *webrtc.TrackLocalStaticRTP
	localAudioTrack     *webrtc.TrackLocalStaticRTP
	dataChannel         *webrtc.DataChannel
	connectedChannel    chan bool
}

func (peer *Peer) getConnection() *webrtc.PeerConnection {
	peer.peerConnectionMutex.RLock()
	defer peer.peerConnectionMutex.RUnlock()
	return peer.peerConnection
}

func (peer *Peer) onICEConnectionStateChange(connectionState webrtc.ICEConnectionState) {
	peer.peerConnectionMutex.RLock()
	defer peer.peerConnectionMutex.RUnlock()

	fmt.Fprintf(os.Stderr,
		"conn %d: state: %s\n",
		peer.connectionId,
		connectionState.String())

	if peer.peerConnection == nil {
		return
	}

	if connectionState == webrtc.ICEConnectionStateConnected {
		peer.connectedChannel <- true
	}
	if connectionState == webrtc.ICEConnectionStateFailed ||
		connectionState == webrtc.ICEConnectionStateClosed {

		peer.connectedChannel <- false
	}
}

func (peer *Peer) close() error {
	func() {
		peer.peerConnectionMutex.RLock()
		defer peer.peerConnectionMutex.RUnlock()

		run := true
		for run {
			select {
			case <-peer.connectedChannel:
			default:
				run = false
			}
		}
	}()

	peer.peerConnectionMutex.Lock()
	defer peer.peerConnectionMutex.Unlock()

	var err error

	if peer.peerConnection != nil {
		err = peer.peerConnection.Close()
		peer.peerConnection = nil
	}

	if err != nil {
		peer.closeDataChannel()
		return err
	}
	return peer.closeDataChannel()
}

func (peer *Peer) closeDataChannel() error {
	if peer.dataChannel != nil {
		peer.dataChannel.OnMessage(func(message webrtc.DataChannelMessage) {
		})

		err := peer.dataChannel.Close()
		peer.dataChannel = nil

		if err != nil {
			return err
		}
	}

	return nil
}

func (peer *Peer) IsClosed() bool {
	peer.peerConnectionMutex.RLock()
	defer peer.peerConnectionMutex.RUnlock()

	return peer.peerConnection == nil
}
