package main

import (
	"fmt"
	"net"
	"os"

	"github.com/pion/webrtc/v4"
)

type Peer struct {
	peerConnection        *webrtc.PeerConnection
	peerConnectionId      int
	localVideoTrack       *webrtc.TrackLocalStaticRTP
	localAudioTrack       *webrtc.TrackLocalStaticRTP
	dataChannel           *webrtc.DataChannel
	remoteVideoConnection *net.UDPConn
	remoteAudioConnection *net.UDPConn
	room                  *Room
	connectedChannel      chan bool
}

func (peer *Peer) onICEConnectionStateChange(connectionState webrtc.ICEConnectionState) {
	peerIndex := 0

	fmt.Fprintf(os.Stderr,
		"conn %d: state - %s\n",
		peer.peerConnectionId,
		connectionState.String())

	if connectionState == webrtc.ICEConnectionStateConnected {
		peer.connectedChannel <- true
	}
	if connectionState == webrtc.ICEConnectionStateFailed ||
		connectionState == webrtc.ICEConnectionStateDisconnected ||
		connectionState == webrtc.ICEConnectionStateClosed {

		if peer.peerConnection != nil {
			peer.connectedChannel <- false
			close(peer.connectedChannel)
		}
		peer.Close(peerIndex)
	}
}

func (peer *Peer) Close(index int) {
	if peer.peerConnection != nil {
		err := peer.peerConnection.Close()
		peer.peerConnection = nil

		if err != nil {
			fmt.Fprintf(os.Stderr,
				"conn %d: peerConnection.Close - %s\n",
				index,
				err)
		}
	}

	if peer.localVideoTrack != nil {
		peer.localVideoTrack = nil
	}

	if peer.localAudioTrack != nil {
		peer.localAudioTrack = nil
	}

	if peer.dataChannel != nil {
		peer.dataChannel.OnMessage(func(message webrtc.DataChannelMessage) {
		})

		err := peer.dataChannel.Close()
		peer.dataChannel = nil

		if err != nil {
			fmt.Fprintf(os.Stderr,
				"conn %d: dataChannel.Close - %s\n",
				index,
				err)
		}
	}

	if peer.remoteAudioConnection != nil {
		err := peer.remoteAudioConnection.Close()
		peer.remoteAudioConnection = nil

		if err != nil {
			fmt.Fprintf(os.Stderr,
				"conn %d: remoteAudioConnection.Close - %s\n",
				index,
				err)
		}
	}

	if peer.remoteVideoConnection != nil {
		err := peer.remoteVideoConnection.Close()
		peer.remoteVideoConnection = nil

		if err != nil {
			fmt.Fprintf(os.Stderr,
				"conn %d: remoteVideoConnection.Close - %s\n",
				index,
				err)
		}
	}
}

func (peer *Peer) CloseRemoteConnections(index int) {
	if peer.dataChannel != nil {
		peer.dataChannel.OnMessage(func(message webrtc.DataChannelMessage) {
		})

		err := peer.dataChannel.Close()
		peer.dataChannel = nil

		if err != nil {
			fmt.Fprintf(os.Stderr,
				"conn %d: dataChannel.Close - %s\n",
				index,
				err)
		}
	}

	if peer.remoteAudioConnection != nil {
		err := peer.remoteAudioConnection.Close()
		peer.remoteAudioConnection = nil

		if err != nil {
			fmt.Fprintf(os.Stderr,
				"conn %d: remoteAudioConnection.Close - %s\n",
				index,
				err)
		}
	}

	if peer.remoteVideoConnection != nil {
		err := peer.remoteVideoConnection.Close()
		peer.remoteVideoConnection = nil

		if err != nil {
			fmt.Fprintf(os.Stderr,
				"conn %d: remoteVideoConnection.Close - %s\n",
				index,
				err)
		}
	}
}

func (peer *Peer) IsNull() bool {
	return (peer.peerConnection == nil ||
		peer.remoteAudioConnection == nil ||
		peer.remoteVideoConnection == nil ||
		peer.dataChannel == nil ||
		peer.localAudioTrack == nil ||
		peer.localVideoTrack == nil)
}
