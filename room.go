package main

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
)

type MeetingType int

const (
	MeetingTypeHost  MeetingType = 0
	MeetingTypeGuest MeetingType = 1
)

type Room struct {
	peers                    map[uint64]*Peer
	peerIndex                int
	signalAccessKey          string
	nextPeerConnectionId     uint64
	localSessionDescription  string
	localCandidates          []string
	remoteSessionDescription string
	remoteCandidates         []string
	mutex                    sync.Mutex
	meetingType              MeetingType
	signalId                 string
	waitForAllICECandidates  <-chan struct{}
	signallingComplete       bool
}

func (room *Room) appendPeer(peer *Peer) {
	room.mutex.Lock()
	defer room.mutex.Unlock()

	room.peers[peer.peerConnectionId] = peer
}

func (room *Room) getPeer(peerconnectionId uint64) *Peer {
	room.mutex.Lock()
	defer room.mutex.Unlock()

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

func (room *Room) initializePeerConnection() error {

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
		peerConnectionId: room.nextPeerConnectionId,
		localVideoTrack:  localVideoTrack,
		localAudioTrack:  localAudioTrack,
		dataChannel:      dataChannel,
		room:             room,
		connectedChannel: make(chan bool),
	}
	room.appendPeer(&peer)

	peerConnection.OnICEConnectionStateChange(peer.onICEConnectionStateChange)

	return nil
}

func (room *Room) closePeer(peerConnectionId uint64) {
	room.getPeer(peerConnectionId).close()
	delete(room.peers, peerConnectionId)
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
	mediaStream *MediaStream,
	peerConnectionId uint64,
	signalServer string) error {

	room.setupTracksAndDataHandlers(mediaStream, peerConnectionId)

	peer := room.getPeer(peerConnectionId)

	var err error
	if room.meetingType == MeetingTypeHost {
		err = room.signalHostPost(signalServer)
		if err != nil {
			return err
		}

		room.waitForAllICECandidates = webrtc.GatheringCompletePromise(peer.peerConnection)

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

func (room *Room) waitForIceConnected(peerConnectionId uint64,
	signalServer string) error {
	const stepWait = 50

	start := time.Now()

	var err error

	for time.Since(start) < 20*time.Second {

		select {
		case connected := <-room.getPeer(peerConnectionId).connectedChannel:
			if connected {
				return nil
			} else {
				return fmt.Errorf("ice disconnected")
			}
		case <-time.After(stepWait * time.Millisecond):
			if len(signalServer) != 0 {
				err = room.signalOperations(signalServer, peerConnectionId)
				if err != nil {
					return err
				}
			}
		}
	}

	return fmt.Errorf("ice connection time out")
}

func (room *Room) waitForIceDisconnected(peerConnectionId uint64,
	signalServer string) error {
	const stepWait = 50

	var err error

	for {
		select {
		case connected := <-room.getPeer(peerConnectionId).connectedChannel:
			if !connected {
				return nil
			} else {
				return fmt.Errorf("ice connected")
			}
		case <-time.After(stepWait * time.Millisecond):
			if len(signalServer) != 0 {
				err = room.signalOperations(signalServer, peerConnectionId)
				if err != nil {
					return err
				}
			}
		}
	}
}

func (room *Room) signalOperations(signalServer string,
	peerConnectionId uint64) error {
	if room.signallingComplete {
		return nil
	}

	if room.meetingType == MeetingTypeHost {
		return room.signalHostOperations(signalServer, peerConnectionId)
	} else {
		return room.signalGuestOperations(signalServer, peerConnectionId)
	}
}

func (room *Room) signalGuestOperations(signalServer string,
	peerConnectionId uint64) error {
	if room.signallingComplete {
		return nil
	}

	var err error
	room.signallingComplete, err = room.signalHostGet(signalServer)
	if err != nil {
		room.signalAccessKey = ""

		if room.signallingComplete {
			room.signallingComplete = false
			return nil
		}
		return err
	}
	room.signallingComplete = false

	peer := room.getPeer(peerConnectionId)

	if len(room.remoteSessionDescription) != 0 {
		var sessionDescription webrtc.SessionDescription
		decode(room.remoteSessionDescription, &sessionDescription)
		err = peer.peerConnection.SetRemoteDescription(sessionDescription)
		if err != nil {
			return err
		}

		room.waitForAllICECandidates = webrtc.GatheringCompletePromise(peer.peerConnection)

		answerSessionDescription, err := peer.peerConnection.CreateAnswer(nil)
		if err != nil {
			return err
		}

		err = peer.peerConnection.SetLocalDescription(answerSessionDescription)
		if err != nil {
			return err
		}
	} else if len(room.remoteCandidates) != 0 {
		// for (const candidate of room.remoteCandidates) {
		//     await peerConnection.addIceCandidate(
		//         JSON.parse(atob(candidate))
		//     );
		// }
	}

	room.remoteSessionDescription = ""
	room.remoteCandidates = []string{}

	select {
	case <-room.waitForAllICECandidates:
		room.waitForAllICECandidates = nil
		localDescription := peer.peerConnection.LocalDescription()
		room.localSessionDescription = encode(localDescription)
	default:
	}

	if len(room.localSessionDescription) != 0 || len(room.localCandidates) != 0 {
		room.signallingComplete, err = room.signalGuestPost(signalServer)
		// room.signallingComplete becoming true regardless of ICE trickling proper
		// implementation might lead to no connection
		if err != nil {
			return err
		}
		room.localSessionDescription = ""
		room.localCandidates = []string{}
	}

	return nil
}

func (room *Room) signalHostOperations(
	signalServer string,
	peerConnectionId uint64) error {
	if room.signallingComplete {
		return nil
	}

	var err error

	select {
	case <-room.waitForAllICECandidates:
		room.waitForAllICECandidates = nil
		localDescription := room.getPeer(peerConnectionId).peerConnection.LocalDescription()
		room.localSessionDescription = encode(localDescription)
	default:
	}

	if len(room.localSessionDescription) != 0 || len(room.localCandidates) != 0 {
		err = room.signalHostPost(signalServer)
		if err != nil {
			room.signallingComplete = true
			return err
		}
		room.localSessionDescription = ""
		room.localCandidates = []string{}
	}

	err = room.signalGuestGet(signalServer)
	if err != nil {
		room.signallingComplete = true
		return err
	}

	if len(room.remoteSessionDescription) != 0 {
		var sessionDescription webrtc.SessionDescription
		decode(room.remoteSessionDescription, &sessionDescription)
		room.getPeer(peerConnectionId).peerConnection.SetRemoteDescription(sessionDescription)

		room.signallingComplete = true
	} else if len(room.remoteCandidates) != 0 {
		// for (const candidate of room.remoteCandidates) {
		//     await peerConnection.addIceCandidate(
		//         JSON.parse(atob(candidate))
		//     );
		// }
	}
	room.remoteSessionDescription = ""
	room.remoteCandidates = []string{}

	return nil
}
