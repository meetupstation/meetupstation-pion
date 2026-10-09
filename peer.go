package main

import (
	"fmt"
	"os"
	"sync"

	"github.com/pion/webrtc/v4"
)

type RemoteTrackGroup struct {
	remoteVideoTrack *webrtc.TrackRemote
	remoteAudioTrack *webrtc.TrackRemote
	localVideoTrack  *webrtc.TrackLocalStaticRTP
	localAudioTrack  *webrtc.TrackLocalStaticRTP
}

type Peer struct {
	peerConnectionMutex sync.RWMutex
	peerConnection      *webrtc.PeerConnection
	connectionId        uint64
	transceiversMutex   sync.RWMutex
	localVideoTrack     *webrtc.TrackLocalStaticRTP
	localAudioTrack     *webrtc.TrackLocalStaticRTP
	remoteTracks        map[string]*RemoteTrackGroup
	remoteTrackMutex    sync.RWMutex
	dataChannel         *webrtc.DataChannel
	connectedChannel    chan struct{}
	closedChannel       chan struct{}
	connectedOnce       sync.Once
	closedOnce          sync.Once
}

func (peer *Peer) getConnection() *webrtc.PeerConnection {
	peer.peerConnectionMutex.RLock()
	defer peer.peerConnectionMutex.RUnlock()
	return peer.peerConnection
}

func (peer *Peer) onICEConnectionStateChange(connectionState webrtc.ICEConnectionState) {
	peer.peerConnectionMutex.RLock()
	defer peer.peerConnectionMutex.RUnlock()

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

func (peer *Peer) onTrack(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
	peer.remoteTrackMutex.Lock()
	defer peer.remoteTrackMutex.Unlock()

	fmt.Printf("Received remote track: ID=%s, Kind=%s, StreamID=%s, Msid=%s\n", track.ID(), track.Kind().String(), track.StreamID(), track.Msid())

	for k, v := range peer.remoteTracks {
		if v.remoteAudioTrack == nil && v.remoteVideoTrack == nil {
			delete(peer.remoteTracks, k)
		}
	}

	streamId := track.StreamID() //track.Msid()
	if _, exists := peer.remoteTracks[streamId]; !exists {
		peer.remoteTracks[streamId] = &RemoteTrackGroup{}
	}
	remoteTrackGroup := peer.remoteTracks[streamId]

	localTrack, err := webrtc.NewTrackLocalStaticRTP(
		track.Codec().RTPCodecCapability,
		track.ID()+"-echo",
		track.StreamID()+"-echo",
	)
	if err != nil {
		fmt.Printf("Failed to create local track: %v\n", err)
	}

	if track.Kind() == webrtc.RTPCodecTypeVideo {
		remoteTrackGroup.remoteVideoTrack = track
		if err == nil {
			remoteTrackGroup.localVideoTrack = localTrack
		} else {
			remoteTrackGroup.localVideoTrack = nil
		}
	} else {
		remoteTrackGroup.remoteAudioTrack = track
		if err == nil {
			remoteTrackGroup.localAudioTrack = localTrack
		} else {
			remoteTrackGroup.localAudioTrack = nil
		}
	}

	peer.setupTransceiverTrack(localTrack)
}

func (peer *Peer) close() error {
	peer.peerConnectionMutex.Lock()
	defer peer.peerConnectionMutex.Unlock()

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

func (peer *Peer) setupTransceiverTrack(track *webrtc.TrackLocalStaticRTP) *webrtc.RTPTransceiver {
	peerConnection := peer.getConnection()

	ro := func() *webrtc.RTPTransceiver {
		peer.transceiversMutex.RLock()
		defer peer.transceiversMutex.RUnlock()

		transceivers := peerConnection.GetTransceivers()

		for index, transceiver := range transceivers {
			if index == 0 || index == 1 {
				continue
			}
			if transceiver.Sender().Track() == track {
				return transceiver
			}
		}

		return nil
	}

	mapTrackGroups := make(map[webrtc.TrackLocal]struct{})
	for _, remoteTrackGroup := range peer.remoteTracks {
		mapTrackGroups[remoteTrackGroup.localAudioTrack] = struct{}{}
		mapTrackGroups[remoteTrackGroup.localVideoTrack] = struct{}{}
	}

	rw := func() *webrtc.RTPTransceiver {
		peer.transceiversMutex.Lock()
		defer peer.transceiversMutex.Unlock()

		transceivers := peerConnection.GetTransceivers()

		for index, transceiver := range transceivers {
			if index == 0 || index == 1 {
				continue
			}
			_, exists := mapTrackGroups[transceiver.Sender().Track()]
			if exists == false && transceiver.Kind() == track.Kind() {
				transceiver.Sender().ReplaceTrack(track)
				return transceiver
			}
		}

		return nil
	}

	if transceiver := ro(); transceiver != nil {
		return transceiver
	}
	return rw()
}

func (peer *Peer) setupTransceiverTrackLocal(track *webrtc.TrackLocalStaticRTP) *webrtc.RTPTransceiver {
	peerConnection := peer.getConnection()

	index := func() int {
		if peerConnection.GetTransceivers()[0].Kind() == track.Kind() {
			return 0
		}
		return 1
	}()
	transceiver := peerConnection.GetTransceivers()[index]
	sender := transceiver.Sender()
	if sender.Track() != track {
		fmt.Println("replaced the local track")
		sender.ReplaceTrack(track)
	}
	return peerConnection.GetTransceivers()[index]
}

func (peer *Peer) deleteTransceiverTrack(track *webrtc.TrackLocalStaticRTP) {
	peerConnection := peer.getConnection()

	peer.transceiversMutex.Lock()
	defer peer.transceiversMutex.Unlock()

	transceivers := peerConnection.GetTransceivers()

	for _, transceiver := range transceivers {
		if transceiver.Sender().Track() == track {
			transceiver.Sender().ReplaceTrack(nil)
			return
		}
	}
}

func (peer *Peer) deleteTransceiverTrackLocal(track *webrtc.TrackLocalStaticRTP) {
	peerConnection := peer.getConnection()

	index := func() int {
		if peerConnection.GetTransceivers()[0].Kind() == track.Kind() {
			return 0
		}
		return 1
	}()
	peerConnection.GetTransceivers()[index].Sender().ReplaceTrack(nil)
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
	peer.peerConnectionMutex.RLock()
	defer peer.peerConnectionMutex.RUnlock()

	return peer.peerConnection == nil
}
