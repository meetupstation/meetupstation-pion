package main

import (
	"errors"
	"fmt"
	"net"
	"os"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

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

func setupTracksAndDataHandlers(peer Peer, peerConnectionId int) {

	var localAddress *net.UDPAddr
	var err error

	localAddress, err = net.ResolveUDPAddr("udp", "127.0.0.1:")
	if err != nil {
		panic(fmt.Sprintf("logic: net.ResolveUDPAddr for local - %s", err))
	}

	var remoteAddressAudio *net.UDPAddr
	remoteAddressAudio, err = net.ResolveUDPAddr("udp", "127.0.0.1:4004")
	if err != nil {
		panic(fmt.Sprintf("logic: net.ResolveUDPAddr for remote audio - %s", err))
	}

	peer.remoteAudioConnection, err = net.DialUDP("udp", localAddress, remoteAddressAudio)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"conn %d: audio - net.DialUDP - %s\n",
			peerConnectionId,
			err)
	}

	var remoteAddressVideo *net.UDPAddr
	remoteAddressVideo, err = net.ResolveUDPAddr("udp", "127.0.0.1:4006")
	if err != nil {
		panic(fmt.Sprintf("logic: net.ResolveUDPAddr for remote video - %s", err))
	}

	peer.remoteVideoConnection, err = net.DialUDP("udp", localAddress, remoteAddressVideo)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"conn %d: video - net.DialUDP - %s\n",
			peerConnectionId,
			err)
	}

	peer.peerConnection.OnTrack(
		func(track *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) {
			connection, payloadType :=
				func(track *webrtc.TrackRemote) (*net.UDPConn, uint8) {
					if track.Kind().String() == "video" {
						return peer.remoteVideoConnection, 96
					} else {
						return peer.remoteAudioConnection, 111
					}
				}(track)

			buf := make([]byte, 1500)
			rtpPacket := &rtp.Packet{}
			for {
				if peer.IsNull() {
					break
				}

				n, _, err := track.Read(buf)
				if err != nil {
					fmt.Fprintf(os.Stderr,
						"conn %d: track read - %s\n",
						peerConnectionId,
						err)
					break
				}

				err = rtpPacket.Unmarshal(buf[:n])
				if err != nil {
					fmt.Fprintf(os.Stderr,
						"conn %d: rtp packet unmarshal - %s\n",
						peerConnectionId,
						err)
				}
				rtpPacket.PayloadType = payloadType

				n, err = rtpPacket.MarshalTo(buf)
				if err != nil {
					fmt.Fprintf(os.Stderr,
						"conn %d: rtp packet marshal - %s\n",
						peerConnectionId,
						err)
				}

				_, err = connection.Write(buf[:n])
				if err != nil {
					var opError *net.OpError
					if errors.As(err, &opError) &&
						opError.Err.Error() == "write: connection refused" {
						continue
					}

					fmt.Fprintf(os.Stderr,
						"conn %d: rtp packet write - %s\n",
						peerConnectionId,
						err)

					break
				}
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

func streamLocalTrack(peers *[]Peer, mediaType MediaType, port int) {
	listener, err := net.ListenUDP(
		"udp",
		&net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: port})

	if err != nil {
		fmt.Fprintf(os.Stderr,
			"net.ListenUDP, %s\n",
			err)
	}

	defer func() {
		if err = listener.Close(); err != nil {
			fmt.Fprintf(os.Stderr,
				"listener.Close, %s\n",
				err)
		}
	}()

	// Increase the UDP receive buffer size
	// Default UDP buffer sizes vary on different operating systems
	bufferSize := 300000 // 300KB
	err = listener.SetReadBuffer(bufferSize)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"listener.SetReadBuffer, %s\n",
			err)
	}

	inboundRTPPacket := make([]byte, 1600) // UDP MTU
	for {
		readBytes, _, err := listener.ReadFrom(inboundRTPPacket)
		if err != nil {
			fmt.Fprintf(os.Stderr,
				"listener.ReadFrom: %s\n",
				err)
		}

		// fmt.Println(readBytes)
		for peerIndex, peer := range *peers {
			track := func() *webrtc.TrackLocalStaticRTP {
				if mediaType == MediaTypeVideo {
					return peer.localVideoTrack
				} else {
					return peer.localAudioTrack
				}
			}()

			if peer.IsNull() {
				continue
			}
			_, err = track.Write(inboundRTPPacket[:readBytes])
			if err != nil {
				// if errors.Is(err, io.ErrClosedPipe) {
				// 	peer.Close(peerIndex)
				// }

				fmt.Fprintf(os.Stderr,
					"conn %d: while write to track: %s\n",
					peerIndex,
					err)
			}
		}
		// fmt.Println(writtenBytes)
	}
}
