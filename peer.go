package main

import (
	"fmt"
	"os"
	"sync"

	"github.com/pion/webrtc/v4"
)

type Peer struct {
	peerConnection   *webrtc.PeerConnection
	connectionId     uint64
	dataChannel      *webrtc.DataChannel
	connectedChannel chan struct{}
	closedChannel    chan struct{}
	connectedOnce    sync.Once
	closedOnce       sync.Once

	streamer *Streamer
}

func (peer *Peer) getConnection() *webrtc.PeerConnection {
	return peer.peerConnection
}

func (peer *Peer) onICEConnectionStateChange(connectionState webrtc.ICEConnectionState) {
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

func (peer *Peer) onRemoteTrack(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	fmt.Fprintf(os.Stdout,
		"conn %d: onRemoteTrack: ID=%s, Kind=%s, StreamID=%s, Msid=%s\n",
		peer.connectionId,
		track.ID(),
		track.Kind().String(),
		track.StreamID(),
		track.Msid())

	err := peer.streamer.tryRemoteStream(track)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"Error: conn %d: onRemoteTrack: %v\n",
			peer.connectionId,
			err)
		peer.streamer.discardRemoteStream(track)
	} else {
		peer.streamer.commitRemoteStream()
	}
}

func (peer *Peer) close() error {
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
	return peer.peerConnection == nil
}

func (peer *Peer) isReady() bool {
	peerConnection := peer.getConnection()
	if peerConnection == nil {
		return false
	}

	state := peerConnection.ICEConnectionState()

	if state != webrtc.ICEConnectionStateConnected &&
		state != webrtc.ICEConnectionStateCompleted {
		return false
	}

	return true
}
