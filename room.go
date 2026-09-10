package main

import (
	"fmt"
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
	peers                    []Peer
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
}

func (room *Room) appendPeer(peer Peer) {
	room.mutex.Lock()
	defer room.mutex.Unlock()

	room.peers = append(room.peers, peer)
}

func (room *Room) getPeer(peerconnectionId int) Peer {
	room.mutex.Lock()
	defer room.mutex.Unlock()

	return room.peers[peerconnectionId]
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
		peerConnection:        peerConnection,
		peerConnectionId:      room.nextPeerConnectionId,
		localVideoTrack:       localVideoTrack,
		localAudioTrack:       localAudioTrack,
		dataChannel:           dataChannel,
		remoteVideoConnection: nil,
		remoteAudioConnection: nil,
		room:                  room,
		connectedChannel:      make(chan bool),
	}
	room.appendPeer(peer)

	peerConnection.OnICEConnectionStateChange(peer.onICEConnectionStateChange)

	return nil
}

func (room *Room) prepareGuestAnswerOrHostOffer(
	peerConnectionId int,
	signalServer string) error {

	room.mutex.Lock()
	for index, peer := range room.peers {
		if index == peerConnectionId {
			continue
		}

		peer.CloseRemoteConnections(index)
	}
	room.mutex.Unlock()

	peer := room.getPeer(peerConnectionId)

	setupTracksAndDataHandlers(peer, peerConnectionId)

	var err error
	if room.meetingType == MeetingTypeHost {
		err = room.signalHostPost(signalServer)
		if err != nil {
			return fmt.Errorf("host already exists probably: %s\n", err)
		}

		offerSessionDescription, err := peer.peerConnection.CreateOffer(nil)

		if err != nil {
			return err
		}

		err = peer.peerConnection.SetLocalDescription(offerSessionDescription)
		if err != nil {
			return err
		}
		room.localSessionDescription = encode(&offerSessionDescription)
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

	if len(room.remoteSessionDescription) != 0 {
		var sessionDescription webrtc.SessionDescription
		decode(room.remoteSessionDescription, &sessionDescription)
		err = room.getPeer(peerConnectionId).peerConnection.SetRemoteDescription(sessionDescription)
		if err != nil {
			return false, err
		}

		answerSessionDescription, err := room.getPeer(peerConnectionId).peerConnection.CreateAnswer(nil)
		if err != nil {
			return false, err
		}

		room.getPeer(peerConnectionId).peerConnection.SetLocalDescription(answerSessionDescription)
		if err != nil {
			return false, err
		}

		room.localSessionDescription = encode(&answerSessionDescription)
	} else if len(room.remoteCandidates) != 0 {
		// for (const candidate of room.remoteCandidates) {
		//     await peerConnection.addIceCandidate(
		//         JSON.parse(atob(candidate))
		//     );
		// }
	}

	room.remoteSessionDescription = ""
	room.remoteCandidates = []string{}

	if len(room.localSessionDescription) != 0 || len(room.localCandidates) != 0 {
		signalCode, err = room.signalGuestPost(signalServer)
		if err != nil && signalCode == false {
			return signalCode, err
		}
		room.localSessionDescription = ""
		room.localCandidates = []string{}
	}

	return signalCode, nil
}

func (room *Room) signalHostOperations(
	signalServer string,
	peerConnectionId int) (bool, error) {

	final := false
	var err error

	if len(room.localSessionDescription) != 0 || len(room.localCandidates) != 0 {
		err = room.signalHostPost(signalServer)

		if err != nil {
			return false, err
		}
		room.localSessionDescription = ""
		room.localCandidates = []string{}
	}

	room.signalGuestGet(signalServer)

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
