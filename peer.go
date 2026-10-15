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
	transceivers        []*webrtc.RTPTransceiver
	localTracks         []*webrtc.TrackLocalStaticRTP
	dataChannel         *webrtc.DataChannel
	connectedChannel    chan struct{}
	closedChannel       chan struct{}
	connectedOnce       sync.Once
	closedOnce          sync.Once

	room *Room
}

func (peer *Peer) getConnection() *webrtc.PeerConnection {
	peer.peerConnectionMutex.RLock()
	defer peer.peerConnectionMutex.RUnlock()
	return peer.peerConnection
}

func (peer *Peer) onICEConnectionStateChange(connectionState webrtc.ICEConnectionState) {
	peer.peerConnectionMutex.RLock()
	defer peer.peerConnectionMutex.RUnlock()

	fmt.Fprintf(os.Stdout,
		"conn %d: state: %s\n",
		peer.connectionId,
		connectionState.String())

	if peer.peerConnection == nil {
		return
	}

	if connectionState == webrtc.ICEConnectionStateConnected {
		peer.connectedOnce.Do(func() { close(peer.connectedChannel) })
	}
	if connectionState == webrtc.ICEConnectionStateFailed ||
		connectionState == webrtc.ICEConnectionStateClosed {
		peer.closedOnce.Do(func() { close(peer.closedChannel) })
	}
}

func (peer *Peer) onTrack(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	fmt.Fprintf(os.Stdout,
		"conn %d: ontrack: ID=%s, Kind=%s, StreamID=%s, Msid=%s\n",
		peer.connectionId,
		track.ID(),
		track.Kind().String(),
		track.StreamID(),
		track.Msid())

	err := peer.room.tryRemoteStream(track)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"Error: conn %d: ontrack: %v\n",
			peer.connectionId,
			err)
		peer.room.discardRemoteStream(track)
	} else {
		peer.room.commitRemoteStream()
	}
}

func (peer *Peer) close() error {
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
