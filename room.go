package main

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
)

type MeetingType int

const (
	MeetingTypeHost  MeetingType = 0
	MeetingTypeGuest MeetingType = 1
)

type Room struct {
	peers             map[uint64]*Peer
	_peerConnectionId uint64
	meetingType       MeetingType
}

func (room *Room) init(meetingType MeetingType) {
	room._peerConnectionId = 1
	room.peers = make(map[uint64]*Peer)
	room.meetingType = meetingType
}

func (room *Room) getAndUpdatePeerConnectionId() uint64 {
	peerConnectionId := room._peerConnectionId

	for {
		room._peerConnectionId++
		if room._peerConnectionId == 0 {
			room._peerConnectionId++
		}

		if _, ok := room.peers[room._peerConnectionId]; !ok {
			break
		}
	}

	return peerConnectionId
}

func (room *Room) appendPeer(peer *Peer) {
	room.peers[peer.connectionId] = peer
}

func (room *Room) getPeer(peerconnectionId uint64) *Peer {
	return room.peers[peerconnectionId]
}

func startPeerConnection() (
	*webrtc.PeerConnection,
	*webrtc.DataChannel,
	error) {

	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{
			{
				URLs: []string{"stun:stun.l.google.com:19302"},
			},
		},
	})
	if err != nil {
		return nil, nil, err
	}

	dataChannelOrdered := true
	dataChannelNegotiated := true
	var dataChannelID uint16 = 0
	dataChannelInit := webrtc.DataChannelInit{
		Ordered:    &dataChannelOrdered,
		Negotiated: &dataChannelNegotiated,
		ID:         &dataChannelID,
	}
	dataChannel, err := peerConnection.CreateDataChannel(
		"meetupstation", &dataChannelInit)
	if err != nil {
		peerConnection.Close()
		return nil, nil, err
	}

	// dataChannel.OnOpen(func() {
	// 	fmt.Fprintf(os.Stderr,
	// 		"data channel opened\n")
	// })
	// dataChannel.OnClose(func() {
	// 	fmt.Fprintf(os.Stderr,
	// 		"data channel closed\n")
	// })

	return peerConnection,
		dataChannel,
		nil
}

func (room *Room) initializePeerConnection(
	peerConnectionId uint64,
	streamer *Streamer) error {

	peerConnection,
		dataChannel,
		err := startPeerConnection()

	if err != nil {
		return err
	}

	peer := Peer{
		peerConnection:   peerConnection,
		connectionId:     peerConnectionId,
		dataChannel:      dataChannel,
		connectedChannel: make(chan struct{}),
		closedChannel:    make(chan struct{}),
		streamer:         streamer,
	}
	room.appendPeer(&peer)

	peerConnection.OnICEConnectionStateChange(peer.onICEConnectionStateChange)

	return nil
}

func (room *Room) closePeer(peerConnectionId uint64) error {
	peer := room.getPeer(peerConnectionId)

	err := peer.close()

	delete(room.peers, peerConnectionId)

	return err
}

func (room *Room) setupTracksAndDataHandlers(
	peerConnectionId uint64,
	streamer *Streamer,
	roomSignalling *SignallingScope) error {

	peer := room.getPeer(peerConnectionId)

	for echoStreamIndex := range len(streamer.echoStreams) {
		err := streamer.echoStreams[echoStreamIndex].outboundTracks.setStreamOnPeer(peer)
		if err != nil {
			return err
		}
	}

	err := streamer.ownStream.outboundTracks.setStreamOnPeer(peer)
	if err != nil {
		return err
	}

	peer.peerConnection.OnTrack(peer.onRemoteTrack)

	peer.peerConnection.OnICECandidate(roomSignalling.OnICECandidate)

	peer.dataChannel.OnClose(
		func() {
		})

	peer.dataChannel.OnMessage(
		func(message webrtc.DataChannelMessage) {
			fmt.Fprintf(os.Stderr,
				"conn %d: data - %s\n",
				peerConnectionId,
				string(message.Data))
		})

	return nil
}

func (room *Room) ifHostPrepareOffer(
	signalling *SignallingScope,
	peerConnectionId uint64) error {

	peer := room.getPeer(peerConnectionId)

	var err error
	if room.meetingType == MeetingTypeHost {
		err = signalling.hostPost("", []string{})
		if err != nil {
			return err
		}

		peerConnection := peer.getConnection()

		offerSessionDescription, err := peerConnection.CreateOffer(nil)

		if err != nil {
			return err
		}

		err = peerConnection.SetLocalDescription(offerSessionDescription)
		if err != nil {
			return err
		}
	}

	return nil
}

func (room *Room) waitForIceConnected(peer *Peer,
	signalling *SignallingScope,
	running *atomic.Bool) error {
	const stepWait = 200

	start := time.Now()

	var err error

	peerConnection := peer.getConnection()

	for time.Since(start) < 20*time.Second && running.Load() {
		select {
		case <-peer.connectedChannel:
			return nil
		case <-peer.closedChannel:
			return fmt.Errorf("ice closed")
		case <-time.After(stepWait * time.Millisecond):
			if signalling != nil {
				signallingComplete := signalling.complete
				err = signalling.polling(room.meetingType, peerConnection)
				if err != nil {
					return err
				}
				if true == signalling.complete && false == signallingComplete {
					fmt.Fprintf(os.Stdout,
						"conn %d: signalling: complete\n",
						peer.connectionId)
				}
			}
		}
	}

	if running.Load() {
		return fmt.Errorf("ice connection time out")
	} else {
		return fmt.Errorf("ice connection shutting down")
	}
}

func (room *Room) waitForIceClosed(peer *Peer,
	signalling *SignallingScope,
	running *atomic.Bool) error {
	const stepWait = 200

	var err error
	peerConnection := peer.getConnection()

	for running.Load() {
		select {
		case <-peer.closedChannel:
			return nil
		case <-time.After(stepWait * time.Millisecond):
			if signalling != nil {
				signallingComplete := signalling.complete
				err = signalling.polling(room.meetingType, peerConnection)
				if err != nil {
					return err
				}
				if true == signalling.complete && false == signallingComplete {
					fmt.Fprintf(os.Stdout,
						"conn %d: signalling: complete\n",
						peer.connectionId)
				}
			}
		}
	}

	return nil
}
