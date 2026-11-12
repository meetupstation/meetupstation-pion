package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync/atomic"
	"syscall"
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

type OutboundTracks struct {
	localTracks []*webrtc.TrackLocalStaticRTP // 0 - video, 1 - audio
}

func (outboundTracks *OutboundTracks) init(
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

	outboundTracks.localTracks = make([]*webrtc.TrackLocalStaticRTP, 2)

	outboundTracks.localTracks[0], err = webrtc.NewTrackLocalStaticRTP(
		rtpCodecCapabilities[0],
		rtpCodecTypes[0].String(),
		streamName)

	if err != nil {
		return err
	}

	outboundTracks.localTracks[1], err = webrtc.NewTrackLocalStaticRTP(
		rtpCodecCapabilities[1],
		rtpCodecTypes[1].String(),
		streamName)

	if err != nil {
		return err
	}

	return nil
}

func (outboundTracks *OutboundTracks) setStreamOnPeer(peer *Peer) error {

	rtpCodecTypes := []webrtc.RTPCodecType{
		webrtc.RTPCodecTypeVideo,
		webrtc.RTPCodecTypeAudio,
	}
	var codecPreferences = [2][]webrtc.RTPCodecParameters{
		{
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
		{
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

		err = transceiver.Sender().ReplaceTrack(outboundTracks.localTracks[index])
		if err != nil {
			return err
		}

		go func() {
			for {
				_, _, err := transceiver.Sender().ReadRTCP()
				if err != nil {
					return
				}
			}
		}()
	}

	return nil
}

type EchoStream struct {
	outboundTracks OutboundTracks
	remoteStreamId string
	remoteTracks   []*webrtc.TrackRemote // 0 - video, 1 - audio
}

func (echoStream *EchoStream) init(streamName string) {
	echoStream.outboundTracks.init(streamName)
	echoStream.remoteStreamId = ""
	echoStream.remoteTracks = make([]*webrtc.TrackRemote, 2)
}

type ReceiveStream struct {
	remoteConnections []*net.UDPConn // 0 - video, 1 - audio

	remoteTracks []*webrtc.TrackRemote // 0 - video, 1 - audio
}

func (receiveStream *ReceiveStream) init(audioPort int, videoPort int) error {
	receiveStream.remoteConnections = make([]*net.UDPConn, 2)

	var err error
	for index := range receiveStream.remoteConnections {
		port := func() int {
			if index == 0 {
				return videoPort
			} else {
				return audioPort
			}
		}()

		receiveStream.remoteConnections[index], err = net.DialUDP(
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

	receiveStream.remoteTracks = make([]*webrtc.TrackRemote, 2)

	return nil
}

func (receiveStream *ReceiveStream) closeRemoteConnections() error {
	var err error = nil

	for index := range receiveStream.remoteConnections {
		if receiveStream.remoteConnections[index] != nil {
			err2 := receiveStream.remoteConnections[index].Close()
			if err == nil && err2 != nil {
				err = err2
			}
		}
	}

	return err
}

func (receiveStream *ReceiveStream) Bind(remoteTrack *webrtc.TrackRemote) {

	trackIndexMap := map[webrtc.RTPCodecType]int{
		webrtc.RTPCodecTypeVideo: 0,
		webrtc.RTPCodecTypeAudio: 1,
	}
	trackIndex := trackIndexMap[remoteTrack.Kind()]
	receiveStream.remoteTracks[trackIndex] = remoteTrack
}

type OwnStream struct {
	outboundTracks OutboundTracks

	listeners []*net.UDPConn // 0 - video, 1 - audio
}

func (ownStream *OwnStream) init(
	streamName string,
	audioPort int,
	videoPort int) error {
	ownStream.outboundTracks.init(streamName)
	ownStream.listeners = make([]*net.UDPConn, 2)

	var err error
	for index := range ownStream.listeners {
		port := func() int {
			if index == 0 {
				return videoPort
			} else {
				return audioPort
			}
		}()

		ownStream.listeners[index], err = net.ListenUDP(
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
		err = ownStream.listeners[index].SetReadBuffer(bufferSize)
		if err != nil {
			return err
		}
	}

	return nil
}

func (ownStream *OwnStream) closeListeners() error {
	var err error = nil
	for index := range ownStream.listeners {
		if ownStream.listeners[index] != nil {
			err2 := ownStream.listeners[index].Close()
			if err == nil && err2 != nil {
				err = err2
			}
		}
	}

	return err
}

type Streamer struct {
	echoStreams   []*EchoStream // size: echo streams
	ownStream     OwnStream
	receiveStream ReceiveStream
}

func (streamer *Streamer) init(
	echoStreams int,
	listenAudioPort int,
	listenVideoPort int,
	remoteAudioPort int,
	remoteVideoPort int,
) error {
	err := streamer.ownStream.init("pion-local", listenAudioPort, listenVideoPort)
	if err != nil {
		return err
	}

	err = streamer.receiveStream.init(remoteAudioPort, remoteVideoPort)
	if err != nil {
		return err
	}
	streamer.echoStreams = make([]*EchoStream, echoStreams)
	for echoStreamIndex := range streamer.echoStreams {
		echoStream := EchoStream{}
		echoStream.init(fmt.Sprintf("pion-%d", echoStreamIndex))
		streamer.echoStreams[echoStreamIndex] = &echoStream
	}
	return nil
}

func (streamer *Streamer) Bind(remoteTrack *webrtc.TrackRemote) error {

	streamId := remoteTrack.StreamID()

	index := streamer.find(streamId)
	if index == len(streamer.echoStreams) {
		index = func() int {
			for index := range streamer.echoStreams {
				if streamer.echoStreams[index].remoteStreamId == "" {
					streamer.echoStreams[index].remoteStreamId = streamId
					return index
				}
			}
			return len(streamer.echoStreams)
		}()
	}

	if index == len(streamer.echoStreams) {
		return fmt.Errorf("all track echo streams are used")
	} else {
		trackIndexMap := map[webrtc.RTPCodecType]int{
			webrtc.RTPCodecTypeVideo: 0,
			webrtc.RTPCodecTypeAudio: 1,
		}
		trackIndex := trackIndexMap[remoteTrack.Kind()]
		streamer.echoStreams[index].remoteTracks[trackIndex] = remoteTrack
	}

	return nil
}

func (streamer *Streamer) Unbind(remoteTrack *webrtc.TrackRemote) {
	trackIndexMap := map[webrtc.RTPCodecType]int{
		webrtc.RTPCodecTypeVideo: 0,
		webrtc.RTPCodecTypeAudio: 1,
	}
	trackIndex := trackIndexMap[remoteTrack.Kind()]

	streamId := remoteTrack.StreamID()

	echoStreamIndex := streamer.find(streamId)

	if echoStreamIndex != len(streamer.echoStreams) {
		streamer.echoStreams[echoStreamIndex].remoteTracks[trackIndex] = nil

		allClosed := true
		for trackIndex := range streamer.echoStreams[echoStreamIndex].remoteTracks {
			if streamer.echoStreams[echoStreamIndex].remoteTracks[trackIndex] != nil {
				allClosed = false
				break
			}
		}

		if allClosed {
			streamer.echoStreams[echoStreamIndex].remoteStreamId = ""
		}
	}
}

func (streamer *Streamer) find(streamId string) int {

	var index int
	for index = range streamer.echoStreams {
		if streamer.echoStreams[index].remoteStreamId == streamId {
			return index
		}
	}

	return len(streamer.echoStreams)
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

		streamer.ownStream.listeners[trackIndex].SetReadDeadline(time.Now().Add(time.Second))

		readBytes, _, err := streamer.ownStream.listeners[trackIndex].ReadFrom(buffer)
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

		localTrack := streamer.ownStream.outboundTracks.localTracks[trackIndex]

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

func (streamer *Streamer) echoRemote(echoStreamIndex int,
	mediaType webrtc.RTPCodecType,
	running *atomic.Bool) {

	rebaser := rtpRebaser{initialized: false}
	var packet *rtp.Packet

	trackIndexMap := map[webrtc.RTPCodecType]int{
		webrtc.RTPCodecTypeVideo: 0,
		webrtc.RTPCodecTypeAudio: 1,
	}
	trackIndex := trackIndexMap[mediaType]

	for running.Load() {

		remoteTrack := streamer.echoStreams[echoStreamIndex].remoteTracks[trackIndex]
		if remoteTrack == nil {
			time.Sleep(time.Second)
			continue
		}

		var err error = nil

		remoteTrack.SetReadDeadline(time.Now().Add(time.Second))
		packet, _, err = remoteTrack.ReadRTP()
		if err != nil {
			if errors.Is(err, io.EOF) {
				streamer.Unbind(remoteTrack)
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

		localTrack := streamer.echoStreams[echoStreamIndex].outboundTracks.localTracks[trackIndex]

		err = localTrack.WriteRTP(packet)
		if err != nil {
			if errors.Is(err, io.EOF) {
				// debug and figure what to do
			} else {
				fmt.Fprintf(os.Stderr,
					"echoRemote %d %s: while writing to track: %v\n",
					echoStreamIndex,
					mediaType.String(),
					err)
			}
		}
	}
}

func (streamer *Streamer) receiveRemote(mediaType webrtc.RTPCodecType,
	running *atomic.Bool) {

	rebaser := rtpRebaser{initialized: false}
	var packet *rtp.Packet
	buffer := make([]byte, 1600) // UDP MTU

	trackIndexMap := map[webrtc.RTPCodecType]int{
		webrtc.RTPCodecTypeVideo: 0,
		webrtc.RTPCodecTypeAudio: 1,
	}
	trackIndex := trackIndexMap[mediaType]

	payloadTypeMap := map[webrtc.RTPCodecType]uint8{
		webrtc.RTPCodecTypeVideo: 96,
		webrtc.RTPCodecTypeAudio: 97,
	}
	payloadType := payloadTypeMap[mediaType]

	connection := streamer.receiveStream.remoteConnections[trackIndex]

	for running.Load() {

		remoteTrack := streamer.receiveStream.remoteTracks[trackIndex]
		if remoteTrack == nil {
			time.Sleep(time.Second)
			continue
		}

		var err error = nil

		remoteTrack.SetReadDeadline(time.Now().Add(time.Second))
		packet, _, err = remoteTrack.ReadRTP()
		if err != nil {
			if errors.Is(err, io.EOF) {
				streamer.Unbind(remoteTrack)
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

		packet.PayloadType = payloadType

		bytes, err := packet.MarshalTo(buffer)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"receiveRemote %s: rtp packet marshal: %v\n",
				mediaType.String(),
				err)
			continue
		}

		_, err = connection.Write(buffer[:bytes])
		if err != nil {
			if errors.Is(err, syscall.ECONNREFUSED) {
				continue
			}

			fmt.Fprintf(os.Stderr,
				"receiveRemote %s: rtp packet write: %v\n",
				mediaType.String(),
				err)
			continue
		}
	}

}
