package main

import (
	"fmt"
	"os"

	"github.com/pion/webrtc/v4"
)

type Peer struct {
	peerConnection     *webrtc.PeerConnection
	peerConnectionId   uint64
	localVideoTrack    *webrtc.TrackLocalStaticRTP
	localAudioTrack    *webrtc.TrackLocalStaticRTP
	dataChannel        *webrtc.DataChannel
	room               *Room
	connectedChannel   chan bool
	signallingComplete bool
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
		connectionState == webrtc.ICEConnectionStateDisconnected ||
		connectionState == webrtc.ICEConnectionStateClosed {

		if peer.peerConnection != nil {
			peer.connectedChannel <- false
		}
	}
}

func (peer *Peer) close() {
	if peer.peerConnection != nil {
		err := peer.peerConnection.Close()
		peer.peerConnection = nil

		if err != nil {
			fmt.Fprintf(os.Stderr,
				"conn %d: peerConnection.Close - %s\n",
				peer.peerConnectionId,
				err)
		}
	}

	if peer.localVideoTrack != nil {
		peer.localVideoTrack = nil
	}

	if peer.localAudioTrack != nil {
		peer.localAudioTrack = nil
	}

	peer.closeDataChannel()
}

func (peer *Peer) closeDataChannel() {
	if peer.dataChannel != nil {
		peer.dataChannel.OnMessage(func(message webrtc.DataChannelMessage) {
		})

		err := peer.dataChannel.Close()
		peer.dataChannel = nil

		if err != nil {
			fmt.Fprintf(os.Stderr,
				"conn %d: dataChannel.Close - %s\n",
				peer.peerConnectionId,
				err)
		}
	}
}

func (peer *Peer) IsNull() bool {
	return (peer.peerConnection == nil ||
		peer.dataChannel == nil ||
		peer.localAudioTrack == nil ||
		peer.localVideoTrack == nil)
}
