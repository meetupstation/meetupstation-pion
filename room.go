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
	peers                    []*Peer
	peerIndex                int
	signalAccessKey          string
	nextPeerConnectionId     int
	localSessionDescription  string
	localCandidates          []string
	remoteSessionDescription string
	remoteCandidates         []string
	mutex                    sync.Mutex
	meetingType              MeetingType
	signalId                 string
	signallingComplete       bool
	waitForAllICECandidates  <-chan struct{}
}

func (room *Room) appendPeer(peer Peer) {
	room.mutex.Lock()
	defer room.mutex.Unlock()

	room.peers = append(room.peers, &peer)
}

func (room *Room) getPeer(peerconnectionId int) *Peer {
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
	room.appendPeer(peer)

	peerConnection.OnICEConnectionStateChange(peer.onICEConnectionStateChange)

	return nil
}

func (room *Room) setupTracksAndDataHandlers(mediaStream *MediaStream, peerConnectionId int) {
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
	peerConnectionId int,
	signalServer string) error {

	// room.mutex.Lock()
	// for index, peer := range room.peers {
	// 	if index == peerConnectionId {
	// 		continue
	// 	}

	// 	peer.closeDataChannel(index)
	// }
	// room.mutex.Unlock()

	room.setupTracksAndDataHandlers(mediaStream, peerConnectionId)

	peer := room.getPeer(peerConnectionId)

	var err error
	if room.meetingType == MeetingTypeHost {
		err = room.signalHostPost(signalServer)
		if err != nil {
			return fmt.Errorf("host already exists probably: %s\n", err)
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

		// room.localSessionDescription = encode(&offerSessionDescription)
	} else {

	}

	return nil
}

func (room *Room) waitForIceConnected(peerConnectionId int,
	signalServer string) error {
	const stepWait = 50
	const timeOut = 60 * 1000 / stepWait

	steps := 0

	var err error

	for steps < timeOut {
		steps++

		select {
		case connected := <-room.getPeer(peerConnectionId).connectedChannel:
			if connected {
				return nil
			} else {
				return fmt.Errorf("ice disconnected\n")
			}
		case <-time.After(stepWait * time.Millisecond):
			if !room.signallingComplete {
				room.signallingComplete, err = room.signalOperations(signalServer, peerConnectionId)
				if err != nil {
					return err
				}
			}
		}
	}

	return fmt.Errorf("ice connection time out\n")
}

func (room *Room) waitForIceDisconnected(peerConnectionId int,
	signalServer string) error {
	const stepWait = 50

	var err error

	for {
		select {
		case connected := <-room.getPeer(peerConnectionId).connectedChannel:
			if !connected {
				return nil
			} else {
				return fmt.Errorf("ice connected\n")
			}
		case <-time.After(stepWait * time.Millisecond):
			if !room.signallingComplete {
				room.signallingComplete, err = room.signalOperations(signalServer, peerConnectionId)
				if err != nil {
					return err
				}
			}
		}
	}
}

func (room *Room) signalOperations(signalServer string,
	peerConnectionId int) (bool, error) {
	if room.meetingType == MeetingTypeHost {
		return room.signalHostOperations(signalServer, peerConnectionId)
	} else {
		return room.signalGuestOperations(signalServer, peerConnectionId)
	}
}

func (room *Room) signalGuestOperations(signalServer string,
	peerConnectionId int) (bool, error) {

	signalCode, err := room.signalHostGet(signalServer)
	if err != nil && signalCode == true {
		return signalCode, nil
	}
	if err != nil {
		return false, err
	}

	peer := room.getPeer(peerConnectionId)

	if len(room.remoteSessionDescription) != 0 {
		var sessionDescription webrtc.SessionDescription
		decode(room.remoteSessionDescription, &sessionDescription)
		err = peer.peerConnection.SetRemoteDescription(sessionDescription)
		if err != nil {
			return false, err
		}

		room.waitForAllICECandidates = webrtc.GatheringCompletePromise(peer.peerConnection)

		answerSessionDescription, err := peer.peerConnection.CreateAnswer(nil)
		if err != nil {
			return false, err
		}

		err = peer.peerConnection.SetLocalDescription(answerSessionDescription)
		if err != nil {
			return false, err
		}

		fmt.Fprintf(os.Stdout, "conn %d: got the remote\n", peerConnectionId)

		// room.localSessionDescription = encode(&answerSessionDescription)
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

	signalCode = false
	if len(room.localSessionDescription) != 0 || len(room.localCandidates) != 0 {
		fmt.Fprintf(os.Stdout, "conn %d: set the local\n", peerConnectionId)
		signalCode, err = room.signalGuestPost(signalServer)
		if err != nil && signalCode == false {
			return signalCode, err
		}
		room.localSessionDescription = ""
		room.localCandidates = []string{}
		room.signalAccessKey = ""
	}

	return signalCode, nil
}

func (room *Room) signalHostOperations(
	signalServer string,
	peerConnectionId int) (bool, error) {

	final := false
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
			return false, err
		}
		room.localSessionDescription = ""
		room.localCandidates = []string{}
	}

	err = room.signalGuestGet(signalServer)
	if err != nil {
		return false, err
	}

	if len(room.remoteSessionDescription) != 0 {
		var sessionDescription webrtc.SessionDescription
		decode(room.remoteSessionDescription, &sessionDescription)
		room.getPeer(peerConnectionId).peerConnection.SetRemoteDescription(sessionDescription)
		final = true
	} else if len(room.remoteCandidates) != 0 {
		// for (const candidate of room.remoteCandidates) {
		//     await peerConnection.addIceCandidate(
		//         JSON.parse(atob(candidate))
		//     );
		// }
	}
	room.remoteSessionDescription = ""
	room.remoteCandidates = []string{}

	return final, nil
}
