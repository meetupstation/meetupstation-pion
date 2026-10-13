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

	remoteTracks         []*webrtc.TrackRemote
	remoteStreamMutex    sync.RWMutex
	remoteTrackStreamIds []string
}

func (room *Room) init(meetingType MeetingType) {
	room._peerConnectionId = 0
	room.peers = make(map[uint64]*Peer)
	room.meetingType = meetingType

	room.remoteTracks = make([]*webrtc.TrackRemote, 8)
	room.remoteTrackStreamIds = make([]string, 4)
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

	room.peers[peer.connectionId] = peer
}

func (room *Room) getPeer(peerconnectionId uint64) *Peer {
	room.peersMutex.RLock()
	defer room.peersMutex.RUnlock()

	return room.peers[peerconnectionId]
}

func rangeIndex2TrackIndex(mediaType webrtc.RTPCodecType, rangeIndex int) int {
	if mediaType == webrtc.RTPCodecTypeVideo {
		return 2 * rangeIndex
	} else {
		return 2*rangeIndex + 1
	}
}

func (room *Room) tryRemoteStream(remoteTrack *webrtc.TrackRemote) error {
	streamId := remoteTrack.StreamID() //track.Msid()

	room.remoteStreamMutex.Lock()

	rangeIndex := func() int {
		for rangeIndex, value := range room.remoteTrackStreamIds {
			if value == streamId {
				room.remoteTracks[rangeIndex2TrackIndex(remoteTrack.Kind(), rangeIndex)] = remoteTrack
				return rangeIndex
			}
		}

		for rangeIndex, value := range room.remoteTrackStreamIds {
			if value == "" {
				room.remoteTrackStreamIds[rangeIndex] = streamId
				room.remoteTracks[rangeIndex2TrackIndex(remoteTrack.Kind(), rangeIndex)] = remoteTrack
				return rangeIndex
			}
		}
		return -1
	}()

	room.peersMutex.Lock()

	if -1 == rangeIndex {
		return fmt.Errorf("all remote track slots are used")
	}

	for _, peer := range room.peers {
		localTrack, err := webrtc.NewTrackLocalStaticRTP(
			remoteTrack.Codec().RTPCodecCapability,
			remoteTrack.Kind().String(),
			fmt.Sprintf("pion-%d", rangeIndex),
		)
		if err != nil {
			return fmt.Errorf("conn %d - failed to create local track: %v\n",
				peer.connectionId,
				err)
		}

		trackIndex := rangeIndex2TrackIndex(remoteTrack.Kind(), rangeIndex)

		err = peer.transceivers[trackIndex].Sender().ReplaceTrack(localTrack)
		if err != nil {
			return fmt.Errorf("conn %d - failed to replace the local track: %v\n",
				peer.connectionId,
				err)
		}

		peer.localTracks[trackIndex] = localTrack
	}

	return nil
}

func (room *Room) commitRemoteStream() {
	room.peersMutex.Unlock()
	room.remoteStreamMutex.Unlock()
}

func (room *Room) discardRemoteStream(remoteTrack *webrtc.TrackRemote) {
	streamId := remoteTrack.StreamID() //track.Msid()

	for rangeIndex, value := range room.remoteTrackStreamIds {
		if value == streamId {

			for _, peer := range room.peers {
				peer.transceivers[2*rangeIndex].Sender().ReplaceTrack(nil)
				peer.transceivers[2*rangeIndex+1].Sender().ReplaceTrack(nil)

				peer.localTracks[2*rangeIndex] = nil
				peer.localTracks[2*rangeIndex+1] = nil
			}
			room.peersMutex.Unlock()

			room.remoteTrackStreamIds[rangeIndex] = ""
			room.remoteTracks[2*rangeIndex] = nil
			room.remoteTracks[2*rangeIndex+1] = nil
		}
	}
	room.remoteStreamMutex.Unlock()
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

func (room *Room) initializePeerConnection(peerConnectionId uint64) error {

	peerConnection,
		dataChannel,
		err := startPeerConnection()

	if err != nil {
		return err
	}

	peer := Peer{
		peerConnection:   peerConnection,
		connectionId:     peerConnectionId,
		transceivers:     make([]*webrtc.RTPTransceiver, 10),
		localTracks:      make([]*webrtc.TrackLocalStaticRTP, 10),
		dataChannel:      dataChannel,
		connectedChannel: make(chan struct{}),
		closedChannel:    make(chan struct{}),
		room:             room,
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

func (room *Room) setupTracksAndDataHandlers(peerConnectionId uint64) error {
	peer := room.getPeer(peerConnectionId)

	for rangeIndex := range len(peer.localTracks) / 2 {
		videoTransceiver, err := peer.peerConnection.AddTransceiverFromKind(
			webrtc.RTPCodecTypeVideo,
			webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendrecv},
		)
		if err != nil {
			return err
		}

		audioTransceiver, err := peer.peerConnection.AddTransceiverFromKind(
			webrtc.RTPCodecTypeAudio,
			webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendrecv},
		)
		if err != nil {
			return err
		}

		videoTrack, err := webrtc.NewTrackLocalStaticRTP(
			webrtc.RTPCodecCapability{
				MimeType: webrtc.MimeTypeH264,
			},
			webrtc.RTPCodecTypeVideo.String(),
			fmt.Sprintf("pion-%d", rangeIndex))

		if err != nil {
			return err
		}

		audioTrack, err := webrtc.NewTrackLocalStaticRTP(
			webrtc.RTPCodecCapability{
				MimeType: webrtc.MimeTypeOpus,
			},
			webrtc.RTPCodecTypeAudio.String(),
			fmt.Sprintf("pion-%d", rangeIndex))

		if err != nil {
			return err
		}

		err = videoTransceiver.Sender().ReplaceTrack(videoTrack)
		if err != nil {
			return err
		}
		err = audioTransceiver.Sender().ReplaceTrack(audioTrack)
		if err != nil {
			return err
		}

		peer.transceivers[2*rangeIndex] = videoTransceiver
		peer.transceivers[2*rangeIndex+1] = audioTransceiver
		peer.localTracks[2*rangeIndex] = nil
		peer.localTracks[2*rangeIndex+1] = nil
	}

	peer.peerConnection.OnTrack(peer.onTrack)

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

func (room *Room) prepareHostOffer(
	signalling *SignallingScope,
	peerConnectionId uint64) error {

	peer := room.getPeer(peerConnectionId)

	var err error
	if room.meetingType == MeetingTypeHost {
		err = signalling.hostPost()
		if err != nil {
			return err
		}

		peerConnection := peer.getConnection()

		signalling.waitForAllICECandidates = webrtc.GatheringCompletePromise(peerConnection)

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
				err = signalling.polling(room.meetingType, peerConnection)
				if err != nil {
					return err
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
				err = signalling.polling(room.meetingType, peerConnection)
				if err != nil {
					return err
				}
			}
		}
	}

	return nil
}
