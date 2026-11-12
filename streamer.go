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

	transceivers []*webrtc.RTPTransceiver // 0 - video, 1 - audio
}

func (outbound *Outbound) init() {
	outbound.transceivers = make([]*webrtc.RTPTransceiver, 2)
}

type OutboundArray struct {
	localTracks []*webrtc.TrackLocalStaticRTP // 0 - video, 1 - audio
	outbound    []Outbound                    // size: peer streams
}

func (outboundArray *OutboundArray) init(
	peerStreams int,
	streamName string) error {
	rtpCodecTypes := []webrtc.RTPCodecType{
		webrtc.RTPCodecTypeVideo,
		webrtc.RTPCodecTypeAudio,
	}
	rtpCodecCapabilities := []webrtc.RTPCodecCapability{
		{MimeType: webrtc.MimeTypeH264},
		{MimeType: webrtc.MimeTypeOpus},
	}

	var err error = nil

	outboundArray.localTracks = make([]*webrtc.TrackLocalStaticRTP, 2)

	outboundArray.localTracks[0], err = webrtc.NewTrackLocalStaticRTP(
		rtpCodecCapabilities[0],
		rtpCodecTypes[0].String(),
		streamName)

	if err != nil {
		return err
	}

	outboundArray.localTracks[1], err = webrtc.NewTrackLocalStaticRTP(
		rtpCodecCapabilities[1],
		rtpCodecTypes[1].String(),
		streamName)

	if err != nil {
		return err
	}

	outboundArray.outbound = make([]Outbound, peerStreams)
	for index := range outboundArray.outbound {
		outboundArray.outbound[index].init()
	}

	return nil
}

func (outboundArray *OutboundArray) findOrBind(peerConnectionId uint64) int {
	result := outboundArray.find(peerConnectionId)
	if result != -1 {
		return result
	}

	for index := range outboundArray.outbound {
		if outboundArray.outbound[index].peerConnectionId == 0 {
			outboundArray.outbound[index].peerConnectionId = peerConnectionId
			return index
		}
	}

	return -1
}

func (outboundArray *OutboundArray) find(peerConnectionId uint64) int {
	for index := range outboundArray.outbound {
		if outboundArray.outbound[index].peerConnectionId == peerConnectionId {
			return index
		}
	}

	return -1
}

func (outboundArray *OutboundArray) setStreamOnPeer(
	peerConnectionId uint64,
	peer *Peer) error {

	rtpCodecTypes := []webrtc.RTPCodecType{
		webrtc.RTPCodecTypeVideo,
		webrtc.RTPCodecTypeAudio,
	}
	var codecPreferences = [2][]webrtc.RTPCodecParameters{
		{ // 0 - video: H264 only
			{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType:    webrtc.MimeTypeH264,
					ClockRate:   90000,
					SDPFmtpLine: "level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e032",
					RTCPFeedback: []webrtc.RTCPFeedback{
						{Type: "goog-remb"},
						{Type: "ccm", Parameter: "fir"},
						{Type: "nack"},
						{Type: "nack", Parameter: "pli"},
					},
				},
			},
		},
		{ // 1 - audio: Opus only
			{
				RTPCodecCapability: webrtc.RTPCodecCapability{
					MimeType:    webrtc.MimeTypeOpus,
					ClockRate:   48000,
					Channels:    2,
					SDPFmtpLine: "minptime=10;useinbandfec=1",
				},
			},
		},
	}

	peerStreamIndex := outboundArray.findOrBind(peerConnectionId)
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

		err = transceiver.SetCodecPreferences(codecPreferences[index])
		if err != nil {
			return err
		}

		err = transceiver.Sender().ReplaceTrack(outboundArray.localTracks[index])
		if err != nil {
			return err
		}

		outbound.transceivers[index] = transceiver
	}

	return nil
}

func (outboundArray *OutboundArray) undoStreamOnPeer(peerConnectionId uint64) {
	peerStreamIndex := outboundArray.find(peerConnectionId)
	if peerStreamIndex != -1 {
		outbound := &outboundArray.outbound[peerStreamIndex]
		outbound.peerConnectionId = 0

		for index := range 2 {
			transceiver := outbound.transceivers[index]
			if transceiver != nil {
				transceiver.Sender().ReplaceTrack(nil)
			}
			outbound.transceivers[index] = nil
		}
	}
}

// func (outboundArray *OutboundArray) setStreamOnTrack(
// 	mediaType webrtc.RTPCodecType) error {

// 	trackIndexMap := map[webrtc.RTPCodecType]int{
// 		webrtc.RTPCodecTypeVideo: 0,
// 		webrtc.RTPCodecTypeAudio: 1,
// 	}
// 	trackIndex := trackIndexMap[mediaType]

// 	for peerStreamIndex := range outboundArray.outbound {
// 		if outboundArray.outbound[peerStreamIndex].peerConnectionId == 0 {
// 			continue
// 		}

// 		transceiver := outboundArray.outbound[peerStreamIndex].transceivers[trackIndex]
// 		if transceiver == nil {
// 			continue
// 		}

// 		err := transceiver.Sender().ReplaceTrack(outboundArray.localTracks[trackIndex])
// 		if err != nil {
// 			return fmt.Errorf("peer stream %d: replacing the local track: %v\n",
// 				peerStreamIndex,
// 				err)
// 		}
// 	}

// 	return nil
// }

// func (outboundArray *OutboundArray) undoStreamOnTrack(mediaType webrtc.RTPCodecType) {
// 	trackIndexMap := map[webrtc.RTPCodecType]int{
// 		webrtc.RTPCodecTypeVideo: 0,
// 		webrtc.RTPCodecTypeAudio: 1,
// 	}
// 	trackIndex := trackIndexMap[mediaType]

// 	for peerStreamIndex := range outboundArray.outbound {
// 		// range over local tracks with different peers
// 		if outboundArray.outbound[peerStreamIndex].peerConnectionId == 0 {
// 			continue
// 		}

// 		transceiver := outboundArray.outbound[peerStreamIndex].transceivers[trackIndex]
// 		if transceiver == nil {
// 			continue
// 		}

// 		transceiver.Sender().ReplaceTrack(nil)
// 	}
// }

type Inbound struct {
	outboundArray OutboundArray

	remoteStreamId    string
	remoteConnections []*net.UDPConn // 0 - video, 1 - audio

	remoteTracks []*webrtc.TrackRemote // 0 - video, 1 - audio
}

func (inbound *Inbound) init(peerStreams int, streamName string) {
	inbound.outboundArray.init(peerStreams, streamName)

	inbound.remoteStreamId = ""
	inbound.remoteConnections = make([]*net.UDPConn, 2)

	inbound.remoteTracks = make([]*webrtc.TrackRemote, 2)
}

type ListenStream struct {
	outboundArray OutboundArray

	listeners []*net.UDPConn // 0 - video, 1 - audio
}

func (listenStream *ListenStream) init(peerStreams int, streamName string) {
	listenStream.outboundArray.init(peerStreams, streamName)
	listenStream.listeners = make([]*net.UDPConn, 2)
}

type Streamer struct {
	inbound           []*Inbound // size: mirror streams
	listenStream      ListenStream
	remoteConnections []*net.UDPConn // 0 - video, 1 - audio
}

func (streamer *Streamer) init(mirrorStreams int, peerStreams int) {
	streamer.listenStream.init(peerStreams, "pion-local")
	streamer.inbound = make([]*Inbound, mirrorStreams)
	for mirrorStreamIndex := range streamer.inbound {
		inbound := Inbound{}
		inbound.init(peerStreams, fmt.Sprintf("pion-%d", mirrorStreamIndex))
		streamer.inbound[mirrorStreamIndex] = &inbound
	}

	streamer.remoteConnections = make([]*net.UDPConn, 2)
}

func (streamer *Streamer) findOrBind(streamId string) int {
	result := streamer.find(streamId)
	if result != -1 {
		return result
	}

	for mirrorStreamIndex := range streamer.inbound {
		if streamer.inbound[mirrorStreamIndex].remoteStreamId == "" {
			streamer.inbound[mirrorStreamIndex].remoteStreamId = streamId

			return mirrorStreamIndex
		}
	}

	return -1
}

func (streamer *Streamer) find(streamId string) int {

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
					"sendLocal %s: listener.ReadFrom: %v\n",
					mediaType.String(),
					err)
				time.Sleep(time.Second)
				continue
			}
		}
		// fmt.Println(readBytes)

		if err = packet.Unmarshal(buffer[:readBytes]); err != nil {
			fmt.Fprintf(os.Stderr,
				"sendLocal %s: packet.Unmarshal: %v\n",
				mediaType.String(),
				err)
			time.Sleep(time.Second)
			continue
		}
		rebaser.process(&packet)

		localTrack := streamer.listenStream.outboundArray.localTracks[trackIndex]

		if localTrack == nil {
			continue
		}

		err = localTrack.WriteRTP(&packet)
		if err != nil {
			if errors.Is(err, io.EOF) {
				// debug and figure what to do
			} else {
				fmt.Fprintf(os.Stderr,
					"sendLocal %s: while writing to track: %v\n",
					mediaType.String(),
					err)
			}
		}
	}
}

func (streamer *Streamer) receiveRemote(mirrorStreamIndex int,
	mediaType webrtc.RTPCodecType,
	running *atomic.Bool) {

	rebaser := rtpRebaser{initialized: false}
	var packet *rtp.Packet
	// buffer := make([]byte, 1600) // UDP MTU

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

		var err error = nil

		remoteTrack.SetReadDeadline(time.Now().Add(time.Second))
		packet, _, err = remoteTrack.ReadRTP()
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

		rebaser.process(packet)

		localTrack := streamer.inbound[mirrorStreamIndex].outboundArray.localTracks[trackIndex]

		err = localTrack.WriteRTP(packet)
		if err != nil {
			if errors.Is(err, io.EOF) {
				// debug and figure what to do
			} else {
				fmt.Fprintf(os.Stderr,
					"receiveRemote %d %s: while writing to track: %v\n",
					mirrorStreamIndex,
					mediaType.String(),
					err)
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

		err := streamer.inbound[mirrorStreamIndex].outboundArray.setStreamOnPeer(
			peerConnectionId,
			peer)
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

	mirrorStreamIndex := streamer.findOrBind(streamId)
	if -1 == mirrorStreamIndex {
		return fmt.Errorf("all track mirror streams are used")
	}

	streamer.inbound[mirrorStreamIndex].remoteTracks[trackIndex] = remoteTrack

	return nil //streamer.inbound[mirrorStreamIndex].outboundArray.setStreamOnTrack(remoteTrack.Kind())
}

func (streamer *Streamer) undoRemoteTrack(remoteTrack *webrtc.TrackRemote) {
	trackIndexMap := map[webrtc.RTPCodecType]int{
		webrtc.RTPCodecTypeVideo: 0,
		webrtc.RTPCodecTypeAudio: 1,
	}
	trackIndex := trackIndexMap[remoteTrack.Kind()]

	streamId := remoteTrack.StreamID() //track.Msid()

	mirrorStreamIndex := streamer.find(streamId)

	if mirrorStreamIndex != -1 {
		// streamer.inbound[mirrorStreamIndex].outboundArray.undoStreamOnTrack(remoteTrack.Kind())

		streamer.inbound[mirrorStreamIndex].remoteTracks[trackIndex] = nil

		allClosed := true
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

	return streamer.listenStream.outboundArray.setStreamOnPeer(
		peerConnectionId,
		peer)
}

func (streamer *Streamer) closePeerLocalStream(peerConnectionId uint64) {
	streamer.listenStream.outboundArray.undoStreamOnPeer(peerConnectionId)
}

// func (streamer *Streamer) setLocalStream(mediaType webrtc.RTPCodecType) error {
// 	return streamer.listenStream.outboundArray.setStreamOnTrack(
// 		mediaType)
// }

// func (streamer *Streamer) undoLocalStream(mediaType webrtc.RTPCodecType) {
// 	streamer.listenStream.outboundArray.undoStreamOnTrack(mediaType)
// }
