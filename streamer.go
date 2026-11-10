package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

type rtpRebaser struct {
	initialized          bool
	SSRC                 uint32
	SequenceNumberOffset uint16
	SequenceNumber       uint16
}

func (rebaser *rtpRebaser) process(packet *rtp.Packet) {
	if !rebaser.initialized {
		rebaser.SSRC = packet.SSRC
		rebaser.SequenceNumber = packet.SequenceNumber - 1
		rebaser.SequenceNumberOffset = 0
		rebaser.initialized = true
	}

	if rebaser.SSRC != packet.SSRC {
		rebaser.SequenceNumberOffset = rebaser.SequenceNumber + 1 - packet.SequenceNumber
		rebaser.SSRC = packet.SSRC
	}

	packet.SequenceNumber += rebaser.SequenceNumberOffset
	rebaser.SequenceNumber = packet.SequenceNumber
}

type Outbound struct {
	peerConnectionId uint64

	transceivers []*webrtc.RTPTransceiver      // 0 - video, 1 - audio
	localTracks  []*webrtc.TrackLocalStaticRTP // 0 - video, 1 - audio
}

func (outbound *Outbound) init() {
	outbound.transceivers = make([]*webrtc.RTPTransceiver, 2)
	outbound.localTracks = make([]*webrtc.TrackLocalStaticRTP, 2)
}

type OutboundArray struct {
	outbound []Outbound // size: peer streams
}

func (outboundArray *OutboundArray) init(peerStreams int) {
	outboundArray.outbound = make([]Outbound, peerStreams)
	for index := range outboundArray.outbound {
		outboundArray.outbound[index].init()
	}
}

func (outboundArray *OutboundArray) find(peerConnectionId uint64) int {
	for index := range outboundArray.outbound {
		if outboundArray.outbound[index].peerConnectionId == peerConnectionId {
			return index
		}
	}

	for index := range outboundArray.outbound {
		if outboundArray.outbound[index].peerConnectionId == 0 {
			outboundArray.outbound[index].peerConnectionId = peerConnectionId
			return index
		}
	}

	return -1
}

func (outboundArray *OutboundArray) findRO(peerConnectionId uint64) int {
	for index := range outboundArray.outbound {
		if outboundArray.outbound[index].peerConnectionId == peerConnectionId {
			return index
		}
	}

	return -1
}

func (outboundArray *OutboundArray) setStreamOnPeer(
	peerConnectionId uint64,
	peer *Peer,
	streamName string,
	rtpCodecTypes []webrtc.RTPCodecType,
	rtpCodecCapabilities []webrtc.RTPCodecCapability,
	keepTrack []bool) error {

	peerStreamIndex := outboundArray.find(peerConnectionId)
	if peerStreamIndex == -1 {
		return fmt.Errorf("all peer streams for local sharing are occupied")
	}
	outbound := &outboundArray.outbound[peerStreamIndex]

	for index := range 2 {
		transceiver, err := peer.peerConnection.AddTransceiverFromKind(
			rtpCodecTypes[index],
			webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendrecv},
		)
		if err != nil {
			return err
		}

		track, err := webrtc.NewTrackLocalStaticRTP(
			rtpCodecCapabilities[index],
			rtpCodecTypes[index].String(),
			streamName)

		if err != nil {
			return err
		}

		err = transceiver.Sender().ReplaceTrack(track)
		if err != nil {
			return err
		}

		outbound.transceivers[index] = transceiver
		if keepTrack[index] {
			outbound.localTracks[index] = track
		} else {
			outbound.localTracks[index] = nil
		}
	}

	return nil
}

func (outboundArray *OutboundArray) undoStreamOnPeer(peerConnectionId uint64) {
	peerStreamIndex := outboundArray.findRO(peerConnectionId)
	if peerStreamIndex != -1 {
		outbound := &outboundArray.outbound[peerStreamIndex]
		outbound.peerConnectionId = 0

		for index := range 2 {
			transceiver := outbound.transceivers[index]
			if transceiver != nil {
				transceiver.Sender().ReplaceTrack(nil)
			}
			outbound.transceivers[index] = nil
			outbound.localTracks[index] = nil
		}
	}
}

func (outboundArray *OutboundArray) setStreamOnTrack(
	mediaType webrtc.RTPCodecType,
	rtpCodecCapability webrtc.RTPCodecCapability,
	streamName string) error {

	trackIndexMap := map[webrtc.RTPCodecType]int{
		webrtc.RTPCodecTypeVideo: 0,
		webrtc.RTPCodecTypeAudio: 1,
	}
	trackIndex := trackIndexMap[mediaType]

	for peerStreamIndex := range outboundArray.outbound {
		if outboundArray.outbound[peerStreamIndex].peerConnectionId == 0 {
			continue
		}

		transceiver := outboundArray.outbound[peerStreamIndex].transceivers[trackIndex]
		if transceiver == nil {
			continue
		}

		localTrack, err := webrtc.NewTrackLocalStaticRTP(
			rtpCodecCapability,
			mediaType.String(),
			streamName)

		if err != nil {
			return fmt.Errorf("peer stream %d: creating the local track: %v\n",
				peerStreamIndex,
				err)
		}

		err = transceiver.Sender().ReplaceTrack(localTrack)
		if err != nil {
			return fmt.Errorf("peer stream %d: replacing the local track: %v\n",
				peerStreamIndex,
				err)
		}

		outboundArray.outbound[peerStreamIndex].localTracks[trackIndex] = localTrack
	}

	return nil
}

func (outboundArray *OutboundArray) undoStreamOnTrack(mediaType webrtc.RTPCodecType) {
	trackIndexMap := map[webrtc.RTPCodecType]int{
		webrtc.RTPCodecTypeVideo: 0,
		webrtc.RTPCodecTypeAudio: 1,
	}
	trackIndex := trackIndexMap[mediaType]

	for peerStreamIndex := range outboundArray.outbound {
		// range over local tracks with different peers
		if outboundArray.outbound[peerStreamIndex].peerConnectionId == 0 {
			continue
		}

		transceiver := outboundArray.outbound[peerStreamIndex].transceivers[trackIndex]
		if transceiver == nil || outboundArray.outbound[peerStreamIndex].localTracks[trackIndex] == nil {
			continue
		}

		outboundArray.outbound[peerStreamIndex].localTracks[trackIndex] = nil
		transceiver.Sender().ReplaceTrack(nil)
	}
}

type Inbound struct {
	outboundArray OutboundArray

	remoteStreamId    string
	remoteConnections []*net.UDPConn // 0 - video, 1 - audio

	remoteTracks []*webrtc.TrackRemote // 0 - video, 1 - audio
}

func (inbound *Inbound) init(peerStreams int) {
	inbound.outboundArray.init(peerStreams)

	inbound.remoteStreamId = ""
	inbound.remoteConnections = make([]*net.UDPConn, 2)

	inbound.remoteTracks = make([]*webrtc.TrackRemote, 2)
}

type ListenStream struct {
	outboundArray OutboundArray

	listeners []*net.UDPConn // 0 - video, 1 - audio
}

func (listenStream *ListenStream) init(peerStreams int) {
	listenStream.outboundArray.init(peerStreams)
	listenStream.listeners = make([]*net.UDPConn, 2)
}

type Streamer struct {
	inbound           []*Inbound // size: mirror streams
	listenStream      ListenStream
	remoteConnections []*net.UDPConn // 0 - video, 1 - audio
}

func (streamer *Streamer) init(mirrorStreams int, peerStreams int) {
	streamer.listenStream.init(peerStreams)
	streamer.inbound = make([]*Inbound, mirrorStreams)
	for mirrorStreamIndex := range streamer.inbound {
		inbound := Inbound{}
		inbound.init(peerStreams)
		streamer.inbound[mirrorStreamIndex] = &inbound
	}

	streamer.remoteConnections = make([]*net.UDPConn, 2)
}

func (streamer *Streamer) find(streamId string) int {
	for mirrorStreamIndex := range streamer.inbound {
		if streamer.inbound[mirrorStreamIndex].remoteStreamId == streamId {
			return mirrorStreamIndex
		}
	}

	for mirrorStreamIndex := range streamer.inbound {
		if streamer.inbound[mirrorStreamIndex].remoteStreamId == "" {
			streamer.inbound[mirrorStreamIndex].remoteStreamId = streamId

			return mirrorStreamIndex
		}
	}

	return -1
}

func (streamer *Streamer) findRO(streamId string) int {

	for mirrorStreamIndex := range streamer.inbound {
		if streamer.inbound[mirrorStreamIndex].remoteStreamId == streamId {
			return mirrorStreamIndex
		}
	}

	return -1
}

func (streamer *Streamer) initializeRemoteStreams(audioPort int, videoPort int) error {
	var err error
	for index := range streamer.remoteConnections {
		port := func() int {
			if index == 0 {
				return videoPort
			} else {
				return audioPort
			}
		}()

		streamer.remoteConnections[index], err = net.DialUDP(
			"udp",
			&net.UDPAddr{
				IP:   net.ParseIP("127.0.0.1"),
				Port: 0,
			},
			&net.UDPAddr{
				IP:   net.ParseIP("127.0.0.1"),
				Port: port,
			})
		if err != nil {
			return err
		}
	}

	return nil
}

func (streamer *Streamer) closeRemoteStreams() error {
	var err error = nil

	for index := range streamer.remoteConnections {
		if streamer.remoteConnections[index] != nil {
			err2 := streamer.remoteConnections[index].Close()
			if err == nil && err2 != nil {
				err = err2
			}
		}
	}

	return err
}

func (streamer *Streamer) initializeListeners(audioPort int, videoPort int) error {
	var err error
	for index := range streamer.listenStream.listeners {
		port := func() int {
			if index == 0 {
				return videoPort
			} else {
				return audioPort
			}
		}()

		streamer.listenStream.listeners[index], err = net.ListenUDP(
			"udp",
			&net.UDPAddr{
				IP:   net.ParseIP("127.0.0.1"),
				Port: port,
			})
		if err != nil {
			return err
		}

		// Increase the UDP receive buffer size
		// Default UDP buffer sizes vary on different operating systems
		bufferSize := 300000 // 300KB
		err = streamer.listenStream.listeners[index].SetReadBuffer(bufferSize)
		if err != nil {
			return err
		}
	}

	return nil
}

func (streamer *Streamer) closeListeners() error {

	var err error = nil
	for index := range streamer.listenStream.listeners {
		if streamer.listenStream.listeners[index] != nil {
			err2 := streamer.listenStream.listeners[index].Close()
			if err == nil && err2 != nil {
				err = err2
			}
		}
	}

	return err
}

func (streamer *Streamer) sendLocal(
	mediaType webrtc.RTPCodecType,
	running *atomic.Bool) {

	trackIndexMap := map[webrtc.RTPCodecType]int{
		webrtc.RTPCodecTypeVideo: 0,
		webrtc.RTPCodecTypeAudio: 1,
	}
	trackIndex := trackIndexMap[mediaType]

	rebaser := rtpRebaser{initialized: false}
	var packet rtp.Packet
	buffer := make([]byte, 1600) // UDP MTU

	for running.Load() {

		streamer.listenStream.listeners[trackIndex].SetReadDeadline(time.Now().Add(time.Second))

		readBytes, _, err := streamer.listenStream.listeners[trackIndex].ReadFrom(buffer)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				continue
			} else if errors.Is(err, net.ErrClosed) {
				// I don't close the listener in the code
				// Not before this goroutine is done
				return
			} else {
				fmt.Fprintf(os.Stderr,
					"sendLocal %s: listener.ReadFrom: %s\n",
					mediaType.String(),
					err)
				time.Sleep(time.Second)
				continue
			}
		}
		// fmt.Println(readBytes)

		if err = packet.Unmarshal(buffer[:readBytes]); err != nil {
			time.Sleep(time.Second)
			continue
		}
		rebaser.process(&packet)

		for peerStreamIndex := range streamer.listenStream.outboundArray.outbound {
			// range over local tracks with different peers

			if streamer.listenStream.outboundArray.outbound[peerStreamIndex].peerConnectionId == 0 {
				continue
			}
			transceiver := streamer.listenStream.outboundArray.outbound[peerStreamIndex].transceivers[trackIndex]
			if transceiver == nil {
				continue
			}

			peerConnectionId := streamer.listenStream.outboundArray.outbound[peerStreamIndex].peerConnectionId

			localTrack := streamer.listenStream.outboundArray.outbound[peerStreamIndex].localTracks[trackIndex]

			if localTrack == nil {
				continue
			}

			err = localTrack.WriteRTP(&packet)
			if err != nil {
				if errors.Is(err, io.EOF) {
					// debug and figure what to do
				} else {
					fmt.Fprintf(os.Stderr,
						"conn %d: sendLocal %d, %s: while writing to track: %v\n",
						peerConnectionId,
						peerStreamIndex,
						mediaType.String(),
						err)
				}
			}
		}
	}
}

func (streamer *Streamer) receiveRemote(mirrorStreamIndex int,
	mediaType webrtc.RTPCodecType,
	running *atomic.Bool) {

	// buf := make([]byte, 1500)

	trackIndex := func(mediaType webrtc.RTPCodecType) int {
		if mediaType == webrtc.RTPCodecTypeVideo {
			return 0
		} else {
			return 1
		}
	}(mediaType)

	for running.Load() {

		remoteTrack := streamer.inbound[mirrorStreamIndex].remoteTracks[trackIndex]
		if remoteTrack == nil {
			time.Sleep(time.Second)
			continue
		}

		remoteTrack.SetReadDeadline(time.Now().Add(time.Second))
		rtpPacket, _, err := remoteTrack.ReadRTP()
		if err != nil {
			if errors.Is(err, io.EOF) {
				streamer.undoRemoteTrack(remoteTrack)
			} else {
				// this is happening when, the phone power button is pressed
				// the video stream stops, while audio continues
				// fmt.Fprintf(os.Stderr,
				// 	"remote stream warning: remote track read - %s\n",
				// 	err)
			}
			continue
		}

		for peerStreamIndex := range streamer.inbound[mirrorStreamIndex].outboundArray.outbound {
			// range over local tracks with different peers

			if streamer.inbound[mirrorStreamIndex].outboundArray.outbound[peerStreamIndex].peerConnectionId == 0 {
				continue
			}
			localTrack := streamer.inbound[mirrorStreamIndex].outboundArray.outbound[peerStreamIndex].localTracks[trackIndex]
			if localTrack == nil {
				continue
			}

			peerConnectionId := streamer.inbound[mirrorStreamIndex].outboundArray.outbound[peerStreamIndex].peerConnectionId

			err = localTrack.WriteRTP(rtpPacket)
			if err != nil {
				if errors.Is(err, io.EOF) {
					// debug and figure what to do
				} else {
					fmt.Fprintf(os.Stderr,
						"conn %d: receiveRemote %d, %s: while writing to track: %v\n",
						peerConnectionId,
						peerStreamIndex,
						mediaType.String(),
						err)
				}
			}
		}

		// if oneRemoteStreamedLocally == false {
		// 	oneRemoteStreamedLocally = true

		// 	connection, payloadType :=
		// 		func(mediaType webrtc.RTPCodecType) (*net.UDPConn, uint8) {
		// 			if mediaType == webrtc.RTPCodecTypeVideo {
		// 				return mediaStream.remoteVideoConnection, 96
		// 			} else {
		// 				return mediaStream.remoteAudioConnection, 97
		// 			}
		// 		}(mediaType)

		// 	rtpPacket.PayloadType = payloadType

		// 	n, err := rtpPacket.MarshalTo(buf)
		// 	if err != nil {
		// 		fmt.Fprintf(os.Stderr,
		// 			"remote stream warning: rtp packet marshal - %s\n",
		// 			err)
		// 		continue
		// 	}

		// 	_, err = connection.Write(buf[:n])
		// 	if err != nil {
		// 		if errors.Is(err, syscall.ECONNREFUSED) {
		// 			continue
		// 		}
		// 		fmt.Fprintf(os.Stderr,
		// 			"remote stream warning: rtp packet write - %s\n",
		// 			err)
		// 		continue
		// 	}
		// }
	}

}
func (streamer *Streamer) drainLocalTransceiverSenders(
	mediaType webrtc.RTPCodecType,
	peerStreamIndex int,
	running *atomic.Bool) {

	buf := make([]byte, 1500)

	trackIndex := func(mediaType webrtc.RTPCodecType) int {
		if mediaType == webrtc.RTPCodecTypeVideo {
			return 0
		} else {
			return 1
		}
	}(mediaType)

	for running.Load() {

		transceiver := streamer.listenStream.outboundArray.outbound[peerStreamIndex].transceivers[trackIndex]
		if transceiver == nil {
			time.Sleep(time.Second)
			continue
		}

		transceiver.Sender().SetReadDeadline(time.Now().Add(time.Second))
		transceiver.Sender().Read(buf)
		// if _, _, err := transceiver.Sender().Read(buf); err != nil {

		// }
	}
}
func (streamer *Streamer) drainRemoteTransceiverSenders(
	mediaType webrtc.RTPCodecType,
	mirrorStreamIndex int,
	peerStreamIndex int,
	running *atomic.Bool) {

	buf := make([]byte, 1500)

	trackIndex := func(mediaType webrtc.RTPCodecType) int {
		if mediaType == webrtc.RTPCodecTypeVideo {
			return 0
		} else {
			return 1
		}
	}(mediaType)

	for running.Load() {

		transceiver := streamer.inbound[mirrorStreamIndex].outboundArray.outbound[peerStreamIndex].transceivers[trackIndex]
		if transceiver == nil {
			time.Sleep(time.Second)
			continue
		}

		transceiver.Sender().SetReadDeadline(time.Now().Add(time.Second))
		transceiver.Sender().Read(buf)
		// if _, _, err := transceiver.Sender().Read(buf); err != nil {

		// }
	}
}

func (streamer *Streamer) setupTracksAndDataHandlersMirroring(
	peerConnectionId uint64,
	peer *Peer) error {

	for mirrorStreamIndex := range len(streamer.inbound) {

		rtpCodecTypes := []webrtc.RTPCodecType{
			webrtc.RTPCodecTypeVideo,
			webrtc.RTPCodecTypeAudio,
		}
		rtpCodecCapabilities := []webrtc.RTPCodecCapability{
			{MimeType: webrtc.MimeTypeH264},
			{MimeType: webrtc.MimeTypeOpus},
		}

		keepTrack := []bool{false, false}

		for index := range 2 {
			remoteTrack := streamer.inbound[mirrorStreamIndex].remoteTracks[index]
			if remoteTrack != nil {
				rtpCodecCapabilities[index] = remoteTrack.Codec().RTPCodecCapability
				keepTrack[index] = true
			}
		}

		err := streamer.inbound[mirrorStreamIndex].outboundArray.setStreamOnPeer(
			peerConnectionId,
			peer,
			fmt.Sprintf("pion-%d", mirrorStreamIndex),
			rtpCodecTypes,
			rtpCodecCapabilities,
			keepTrack)
		if err != nil {
			return err
		}
	}

	return nil
}

func (streamer *Streamer) closePeerMirroring(peerConnectionId uint64) {
	for mirrorStreamIndex := range len(streamer.inbound) {
		streamer.inbound[mirrorStreamIndex].outboundArray.undoStreamOnPeer(peerConnectionId)
	}
}

func (streamer *Streamer) onRemoteTrack(remoteTrack *webrtc.TrackRemote) error {
	streamId := remoteTrack.StreamID() //track.Msid()

	trackIndexMap := map[webrtc.RTPCodecType]int{
		webrtc.RTPCodecTypeVideo: 0,
		webrtc.RTPCodecTypeAudio: 1,
	}
	trackIndex := trackIndexMap[remoteTrack.Kind()]

	mirrorStreamIndex := streamer.find(streamId)
	if -1 == mirrorStreamIndex {
		return fmt.Errorf("all track mirror streams are used")
	}

	streamer.inbound[mirrorStreamIndex].remoteTracks[trackIndex] = remoteTrack

	return streamer.inbound[mirrorStreamIndex].outboundArray.setStreamOnTrack(
		remoteTrack.Kind(),
		remoteTrack.Codec().RTPCodecCapability,
		fmt.Sprintf("pion-%d", mirrorStreamIndex))
}

func (streamer *Streamer) undoRemoteTrack(remoteTrack *webrtc.TrackRemote) {
	trackIndexMap := map[webrtc.RTPCodecType]int{
		webrtc.RTPCodecTypeVideo: 0,
		webrtc.RTPCodecTypeAudio: 1,
	}
	trackIndex := trackIndexMap[remoteTrack.Kind()]

	streamId := remoteTrack.StreamID() //track.Msid()

	mirrorStreamIndex := streamer.findRO(streamId)

	if mirrorStreamIndex != -1 {
		streamer.inbound[mirrorStreamIndex].outboundArray.undoStreamOnTrack(remoteTrack.Kind())

		allClosed := true
		streamer.inbound[mirrorStreamIndex].remoteTracks[trackIndex] = nil

		for trackIndex := range streamer.inbound[mirrorStreamIndex].remoteTracks {
			if streamer.inbound[mirrorStreamIndex].remoteTracks[trackIndex] != nil {
				allClosed = false
				break
			}
		}

		if allClosed {
			streamer.inbound[mirrorStreamIndex].remoteStreamId = ""
		}
	}
}

func (streamer *Streamer) setupTracksAndDataHandlersLocalStream(
	peerConnectionId uint64,
	peer *Peer) error {

	rtpCodecTypes := []webrtc.RTPCodecType{
		webrtc.RTPCodecTypeVideo,
		webrtc.RTPCodecTypeAudio,
	}
	rtpCodecCapabilities := []webrtc.RTPCodecCapability{
		{MimeType: webrtc.MimeTypeH264},
		{MimeType: webrtc.MimeTypeOpus},
	}

	return streamer.listenStream.outboundArray.setStreamOnPeer(
		peerConnectionId,
		peer,
		"pion-local",
		rtpCodecTypes,
		rtpCodecCapabilities,
		[]bool{true, true})
}

func (streamer *Streamer) closePeerLocalStream(peerConnectionId uint64) {
	streamer.listenStream.outboundArray.undoStreamOnPeer(peerConnectionId)
}

func (streamer *Streamer) setLocalStream(mediaType webrtc.RTPCodecType) error {

	mimeTypeStringMap := map[webrtc.RTPCodecType]string{
		webrtc.RTPCodecTypeVideo: webrtc.MimeTypeH264,
		webrtc.RTPCodecTypeAudio: webrtc.MimeTypeOpus,
	}
	mimeTypeString := mimeTypeStringMap[mediaType]

	return streamer.listenStream.outboundArray.setStreamOnTrack(
		mediaType,
		webrtc.RTPCodecCapability{
			MimeType: mimeTypeString,
		},
		"pion-local")
}

func (streamer *Streamer) undoLocalStream(mediaType webrtc.RTPCodecType) {
	streamer.listenStream.outboundArray.undoStreamOnTrack(mediaType)
}
