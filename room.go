package main

import (
	"fmt"
	"os"
	"sync"
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
	peersMutex        sync.RWMutex
	_peerConnectionId uint64
	meetingType       MeetingType
}

func (room *Room) init(meetingType MeetingType) {
	room._peerConnectionId = 0
	room.peers = make(map[uint64]*Peer)
	room.meetingType = meetingType
}
func (room *Room) getAndUpdatePeerConnectionId() uint64 {
	peerConnectionId := room._peerConnectionId

	room.peersMutex.RLock()
	defer room.peersMutex.RUnlock()

	room._peerConnectionId++
	for {
		if _, ok := room.peers[room._peerConnectionId]; !ok {
			break
		}

		room._peerConnectionId++
	}

	return peerConnectionId
}

func (room *Room) appendPeer(peer *Peer) {
	room.peersMutex.Lock()
	defer room.peersMutex.Unlock()

	room.peers[peer.peerConnectionId] = peer
}

func (room *Room) getPeer(peerconnectionId uint64) *Peer {
	room.peersMutex.RLock()
	defer room.peersMutex.RUnlock()

	return room.peers[peerconnectionId]
}

func startPeerConnection() (
	*webrtc.PeerConnection,
	*webrtc.TrackLocalStaticRTP,
	*webrtc.TrackLocalStaticRTP,
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
		return nil, nil, nil, nil, err
	}

	videoTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeH264,
		},
		"video",
		"pion")

	if err != nil {
		peerConnection.Close()
		return nil, nil, nil, nil, err
	}

	rtpSender, err := peerConnection.AddTrack(videoTrack)
	if err != nil {
		peerConnection.Close()
		return nil, nil, nil, nil, err
	}
	_ = rtpSender

	audioTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{
			MimeType: webrtc.MimeTypeOpus,
		},
		"audio",
		"pion")

	if err != nil {
		peerConnection.Close()
		return nil, nil, nil, nil, err
	}

	rtpSender, err = peerConnection.AddTrack(audioTrack)
	if err != nil {
		peerConnection.Close()
		return nil, nil, nil, nil, err
	}
	_ = rtpSender

	// // Read incoming RTCP packets
	// // Before these packets are returned they are processed by interceptors. For things
	// // like NACK this needs to be called.
	// go func() {
	// 	rtcpBuf := make([]byte, 1500)
	// 	for {
	// 		if _, _, rtcpErr := rtpSender.Read(rtcpBuf); rtcpErr != nil {
	// 			return
	// 		}
	// 	}
	// }()

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
		return nil, nil, nil, nil, err
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
		videoTrack,
		audioTrack,
		dataChannel,
		nil
}

func (room *Room) initializePeerConnection(peerConnectionId uint64) error {

	peerConnection,
		localVideoTrack,
		localAudioTrack,
		dataChannel,
		err := startPeerConnection()

	if err != nil {
		return err
	}

	peer := Peer{
		peerConnection:   peerConnection,
		peerConnectionId: peerConnectionId,
		localVideoTrack:  localVideoTrack,
		localAudioTrack:  localAudioTrack,
		dataChannel:      dataChannel,
		connectedChannel: make(chan bool),
	}
	room.appendPeer(&peer)

	peerConnection.OnICEConnectionStateChange(peer.onICEConnectionStateChange)

	return nil
}

func (room *Room) closePeer(peerConnectionId uint64) error {
	peer := room.getPeer(peerConnectionId)

	err := peer.close()

	room.peersMutex.Lock()
	defer room.peersMutex.Unlock()
	delete(room.peers, peerConnectionId)
	return err
}

func (room *Room) setupTracksAndDataHandlers(mediaStream *MediaStream, peerConnectionId uint64) {
	peer := room.getPeer(peerConnectionId)

	peer.peerConnection.OnTrack(func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
		if track.Kind().String() == "video" {
			mediaStream.remoteVideoMutex.Lock()
			defer mediaStream.remoteVideoMutex.Unlock()
			mediaStream.remoteVideoTrack = track
		} else {
			mediaStream.remoteAudioMutex.Lock()
			defer mediaStream.remoteAudioMutex.Unlock()
			mediaStream.remoteAudioTrack = track
		}
	})

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
}

func (room *Room) prepareGuestAnswerOrHostOffer(
	signalling *SignallingScope,
	mediaStream *MediaStream,
	peerConnectionId uint64) error {

	room.setupTracksAndDataHandlers(mediaStream, peerConnectionId)

	peer := room.getPeer(peerConnectionId)

	var err error
	if room.meetingType == MeetingTypeHost {
		err = signalling.hostPost()
		if err != nil {
			return err
		}

		signalling.waitForAllICECandidates = webrtc.GatheringCompletePromise(peer.peerConnection)

		offerSessionDescription, err := peer.peerConnection.CreateOffer(nil)

		if err != nil {
			return err
		}

		err = peer.peerConnection.SetLocalDescription(offerSessionDescription)
		if err != nil {
			return err
		}
	} else {

	}

	return nil
}

func (room *Room) waitForIceConnected(peer *Peer,
	signalling *SignallingScope,
	running *atomic.Bool) error {
	const stepWait = 50

	start := time.Now()

	var err error

	for time.Since(start) < 20*time.Second && running.Load() {

		select {
		case connected := <-peer.connectedChannel:
			if connected {
				return nil
			} else {
				return fmt.Errorf("ice closed")
			}
		case <-time.After(stepWait * time.Millisecond):
			if signalling != nil {
				err = signalling.polling(peer.peerConnectionId, room.meetingType, peer.peerConnection)
				if err != nil {
					return err
				}
			}
		}
	}

	return fmt.Errorf("ice connection time out")
}

func (room *Room) waitForIceClosed(peer *Peer,
	signalling *SignallingScope,
	running *atomic.Bool) error {
	const stepWait = 50

	var err error

	for running.Load() {
		select {
		case connected := <-peer.connectedChannel:
			if !connected {
				return nil
			} else {
				return fmt.Errorf("ice connected")
			}
		case <-time.After(stepWait * time.Millisecond):
			if signalling != nil {
				err = signalling.polling(peer.peerConnectionId, room.meetingType, peer.peerConnection)
				if err != nil {
					return err
				}
			}
		}
	}

	return nil
}
