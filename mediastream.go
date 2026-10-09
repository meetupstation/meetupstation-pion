package main

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/pion/webrtc/v4"
)

type MediaStream struct {
	remoteVideoConnection *net.UDPConn
	remoteAudioConnection *net.UDPConn
	localVideoListener    *net.UDPConn
	localAudioListener    *net.UDPConn
}

func (mediaStream *MediaStream) initializeRemoteStreams(audioPort int, videoPort int) error {
	var err error
	mediaStream.remoteAudioConnection, err = net.DialUDP(
		"udp",
		&net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: 0,
		},
		&net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: audioPort,
		})
	if err != nil {
		return err
	}

	mediaStream.remoteVideoConnection, err = net.DialUDP(
		"udp",
		&net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: 0,
		},
		&net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: videoPort,
		})
	if err != nil {
		return err
	}

	return nil
}

func (mediaStream *MediaStream) closeRemoteStreams() error {

	if mediaStream.remoteAudioConnection != nil {
		err := mediaStream.remoteAudioConnection.Close()
		mediaStream.remoteAudioConnection = nil

		if err != nil {
			return err
		}
	}

	if mediaStream.remoteVideoConnection != nil {
		err := mediaStream.remoteVideoConnection.Close()
		mediaStream.remoteVideoConnection = nil

		if err != nil {
			return err
		}
	}

	return nil
}

func (mediaStream *MediaStream) initializeLocalStreams(audioPort int, videoPort int) error {
	var err error
	mediaStream.localAudioListener, err = net.ListenUDP(
		"udp",
		&net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: audioPort,
		})
	if err != nil {
		return err
	}

	mediaStream.localVideoListener, err = net.ListenUDP(
		"udp",
		&net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: videoPort,
		})
	if err != nil {
		return err
	}

	// Increase the UDP receive buffer size
	// Default UDP buffer sizes vary on different operating systems
	bufferSize := 300000 // 300KB
	err = mediaStream.localAudioListener.SetReadBuffer(bufferSize)
	if err != nil {
		return err
	}
	err = mediaStream.localVideoListener.SetReadBuffer(bufferSize)
	if err != nil {
		return err
	}

	return nil
}

func (mediaStream *MediaStream) closeLocalStreams() error {

	if mediaStream.localAudioListener != nil {
		err := mediaStream.localAudioListener.Close()
		mediaStream.localAudioListener = nil

		if err != nil {
			return err
		}
	}

	if mediaStream.localVideoListener != nil {
		err := mediaStream.localVideoListener.Close()
		mediaStream.localVideoListener = nil

		if err != nil {
			return err
		}
	}

	return nil
}

func skipPeer(peer *Peer) bool {
	peerConnection := peer.getConnection()
	if peerConnection == nil {
		return true
	}

	state := peerConnection.ICEConnectionState()

	if state != webrtc.ICEConnectionStateConnected &&
		state != webrtc.ICEConnectionStateCompleted {
		return true
	}

	return false
}

func (mediaStream *MediaStream) sendLocal(room *Room,
	mediaType MediaType,
	running *atomic.Bool) {

	listener := func(mediaType MediaType) *net.UDPConn {
		if mediaType == MediaTypeAudio {
			return mediaStream.localAudioListener
		} else {
			return mediaStream.localVideoListener
		}
	}(mediaType)

	inboundRTPPacket := make([]byte, 1600) // UDP MTU
	for running.Load() {
		eofStream := false

		listener.SetReadDeadline(time.Now().Add(time.Second))

		readBytes, _, err := listener.ReadFrom(inboundRTPPacket)
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				eofStream = true
			} else if errors.Is(err, net.ErrClosed) {
				fmt.Fprintf(os.Stderr, "listener.ReadFrom: %s\n", err)
				return
			} else {
				fmt.Fprintf(os.Stderr, "listener.ReadFrom: %s\n", err)
				time.Sleep(time.Second)
				continue
			}
		}
		// fmt.Println(readBytes)

		room.peersMutex.RLock()
		peers := make(map[uint64]*Peer, len(room.peers))
		maps.Copy(peers, room.peers)
		room.peersMutex.RUnlock()

		if eofStream {
			for /*peerConnectionId*/ _, peer := range peers {
				if mediaType == MediaTypeVideo {
					if peer.localVideoTrack != nil {
						peer.deleteTransceiverTrackLocal(peer.localVideoTrack)
						peer.localVideoTrack = nil
					}
				} else {
					if peer.localAudioTrack != nil {
						peer.deleteTransceiverTrackLocal(peer.localAudioTrack)
						peer.localAudioTrack = nil
					}
				}
			}
			time.Sleep(time.Second)
			continue
		}

		for peerConnectionId, peer := range peers {

			if skipPeer(peer) {
				continue
			}

			track := func() *webrtc.TrackLocalStaticRTP {
				if mediaType == MediaTypeVideo {
					if peer.localVideoTrack == nil {
						videoTrack, err := webrtc.NewTrackLocalStaticRTP(
							webrtc.RTPCodecCapability{
								MimeType: webrtc.MimeTypeH264,
							},
							"video",
							"pion")

						if err != nil {
							return nil
						}

						peer.localVideoTrack = videoTrack
					}
					return peer.localVideoTrack
				} else {
					if peer.localAudioTrack == nil {
						audioTrack, err := webrtc.NewTrackLocalStaticRTP(
							webrtc.RTPCodecCapability{
								MimeType: webrtc.MimeTypeOpus,
							},
							"audio",
							"pion")

						if err != nil {
							return nil
						}
						peer.localAudioTrack = audioTrack
					}
					return peer.localAudioTrack
				}
			}()

			if track == nil {
				continue
			}

			transceiver := peer.setupTransceiverTrackLocal(track)
			if transceiver == nil {
				continue
			}

			// var writtenBytes int
			_, err = track.Write(inboundRTPPacket[:readBytes])
			if err != nil {
				fmt.Fprintf(os.Stderr,
					"conn %d: while write to track: %s\n",
					peerConnectionId,
					err)
			}

			// fmt.Println(writtenBytes)
		}
	}
}

func (mediaStream *MediaStream) receiveRemote(room *Room,
	mediaType MediaType,
	running *atomic.Bool) {

	buf := make([]byte, 1500)
	for running.Load() {

		didSomeStreaming := false
		oneRemoteStreamedLocally := false

		func() {
			room.peersMutex.RLock()
			defer room.peersMutex.RUnlock()

			for /*peerConnectionId*/ _, peer := range room.peers {
				func() {
					if skipPeer(peer) {
						return
					}

					peer.remoteTrackMutex.RLock()
					defer peer.remoteTrackMutex.RUnlock()

					for /*trackId*/ _, remoteTrackGroup := range peer.remoteTracks {
						connection, payloadType :=
							func(mediaType MediaType) (*net.UDPConn, uint8) {
								if mediaType == MediaTypeVideo {
									return mediaStream.remoteVideoConnection, 96
								} else {
									return mediaStream.remoteAudioConnection, 111
								}
							}(mediaType)
						remoteTrack := func() *webrtc.TrackRemote {
							if mediaType == MediaTypeVideo {
								return remoteTrackGroup.remoteVideoTrack
							} else {
								return remoteTrackGroup.remoteAudioTrack
							}
						}()
						setRemoteTrackNil := func() {
							if mediaType == MediaTypeVideo {
								remoteTrackGroup.remoteVideoTrack = nil
							} else {
								remoteTrackGroup.remoteAudioTrack = nil
							}
						}
						/*localTrack*/ _ = func() *webrtc.TrackLocalStaticRTP {
							if mediaType == MediaTypeVideo {
								return remoteTrackGroup.localVideoTrack
							} else {
								return remoteTrackGroup.localAudioTrack
							}
						}()
						setLocalTrackNil := func() {
							if mediaType == MediaTypeVideo {
								remoteTrackGroup.localVideoTrack = nil
							} else {
								remoteTrackGroup.localAudioTrack = nil
							}
						}

						if remoteTrack == nil {
							return
						}

						rtpPacket, _, err := remoteTrack.ReadRTP()
						if err != nil {
							if errors.Is(err, io.EOF) {
								setRemoteTrackNil()
								setLocalTrackNil()
							} else {
								fmt.Fprintf(os.Stderr,
									"remote stream warning: remote track read - %s\n",
									err)
							}
							return
						}

						// if localTrack == nil {
						// 	return
						// }

						// // transceiver := peer.findTransceiver(localTrack)
						// // if transceiver == nil {
						// // 	return
						// // }

						// err = localTrack.WriteRTP(rtpPacket)
						// if err != nil {
						// 	if errors.Is(err, io.EOF) {
						// 		setLocalTrackNil()
						// 	} else {
						// 		fmt.Fprintf(os.Stderr,
						// 			"remote stream warning: local track write - %s\n",
						// 			err)
						// 	}
						// 	return
						// }

						if oneRemoteStreamedLocally == false {
							oneRemoteStreamedLocally = true

							rtpPacket.PayloadType = payloadType

							n, err := rtpPacket.MarshalTo(buf)
							if err != nil {
								fmt.Fprintf(os.Stderr,
									"remote stream warning: rtp packet marshal - %s\n",
									err)
								return
							}

							_, err = connection.Write(buf[:n])
							if err != nil {
								if errors.Is(err, syscall.ECONNREFUSED) {
									return
								}
								fmt.Fprintf(os.Stderr,
									"remote stream warning: rtp packet write - %s\n",
									err)
								return
							}
						}

						didSomeStreaming = true
					}
				}()
			}
		}()

		if didSomeStreaming == false {
			// time.Sleep(time.Second)
		}
	}

	// buf := make([]byte, 1500)
	// rtpPacket := &rtp.Packet{}
	// for running.Load() {
	// 	getStringMediaType := func(mediaType MediaType) string {
	// 		if mediaType == MediaTypeVideo {
	// 			return "video"
	// 		} else {
	// 			return "audio"
	// 		}
	// 	}
	// 	track, connection, payloadType :=
	// 		func(mediaType MediaType) (*webrtc.TrackRemote, *net.UDPConn, uint8) {
	// 			if mediaType == MediaTypeVideo {
	// 				mediaStream.remoteVideoMutex.Lock()
	// 				defer mediaStream.remoteVideoMutex.Unlock()
	// 				return mediaStream.remoteVideoTrack, mediaStream.remoteVideoConnection, 96
	// 			} else {
	// 				mediaStream.remoteAudioMutex.Lock()
	// 				defer mediaStream.remoteAudioMutex.Unlock()
	// 				return mediaStream.remoteAudioTrack, mediaStream.remoteAudioConnection, 111
	// 			}
	// 		}(mediaType)

	// 	if track == nil {
	// 		// fmt.Fprintf(os.Stderr,
	// 		// 	"remote stream warning: nil remote track - %s\n",
	// 		// 	getStringMediaType(mediaType))
	// 		time.Sleep(time.Second)
	// 		continue
	// 	}
	// 	if connection == nil {
	// 		fmt.Fprintf(os.Stderr,
	// 			"remote stream error: nil remote connection - %s\n",
	// 			getStringMediaType(mediaType))
	// 		return
	// 	}

	// 	track.SetReadDeadline(time.Now().Add(time.Second))

	// 	n, _, err := track.Read(buf)
	// 	if err != nil {
	// 		var netErr net.Error
	// 		if errors.As(err, &netErr) && netErr.Timeout() {
	// 			continue
	// 		}
	// 		fmt.Fprintf(os.Stderr,
	// 			"remote stream warning: track read - %s\n",
	// 			err)
	// 		time.Sleep(time.Second)
	// 		continue
	// 	}

	// 	err = rtpPacket.Unmarshal(buf[:n])
	// 	if err != nil {
	// 		fmt.Fprintf(os.Stderr,
	// 			"remote stream warning: rtp packet unmarshal - %s\n",
	// 			err)
	// 		continue
	// 	}

	// 	rtpPacket.PayloadType = payloadType

	// 	n, err = rtpPacket.MarshalTo(buf)
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
