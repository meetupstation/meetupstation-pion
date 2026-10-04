package main

import (
	"fmt"
	"os"

	"github.com/pion/webrtc/v4"
)

type Peer struct {
	peerConnection   *webrtc.PeerConnection
	peerConnectionId uint64
	localVideoTrack  *webrtc.TrackLocalStaticRTP
	localAudioTrack  *webrtc.TrackLocalStaticRTP
	dataChannel      *webrtc.DataChannel
	connectedChannel chan bool
}

func (peer *Peer) onICEConnectionStateChange(connectionState webrtc.ICEConnectionState) {
	fmt.Fprintf(os.Stderr,
		"conn %d: state: %s\n",
		peer.peerConnectionId,
		connectionState.String())

	if connectionState == webrtc.ICEConnectionStateConnected {
		if peer.peerConnection != nil {
			peer.connectedChannel <- true
		}
	}
	if connectionState == webrtc.ICEConnectionStateFailed ||
		connectionState == webrtc.ICEConnectionStateClosed {

		if peer.peerConnection != nil {
			peer.connectedChannel <- false
		}
	}
}

func (peer *Peer) close() error {
	var err error

	if peer.peerConnection != nil {
		err = peer.peerConnection.Close()
		peer.peerConnection = nil

		if err != nil {
			return err
		}
	}

	if peer.localVideoTrack != nil {
		peer.localVideoTrack = nil
	}

	if peer.localAudioTrack != nil {
		peer.localAudioTrack = nil
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

func (peer *Peer) IsNull() bool {
	return (peer.peerConnection == nil ||
		peer.dataChannel == nil ||
		peer.localAudioTrack == nil ||
		peer.localVideoTrack == nil)
}
