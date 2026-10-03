package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type MediaType int

const (
	MediaTypeAudio MediaType = 0
	MediaTypeVideo MediaType = 1
)

func main() {

	var room Room
	var mediaStream MediaStream

	room.nextPeerConnectionId = 0
	room.signalAccessKey = ""
	room.peers = make(map[uint64]*Peer)

	if len(os.Args) != 4 ||
		(os.Args[1] != "host" && os.Args[1] != "guest") {
		fmt.Fprintf(os.Stderr,
			"example usage: ./meetupstation-pion [host,guest] https://meetupstation.com \"secret host room id\"\n")
		return
	}

	switch os.Args[1] {
	case "host":
		room.meetingType = MeetingTypeHost
	case "guest":
		room.meetingType = MeetingTypeGuest
	}

	signalServer := os.Args[2]
	room.signalId = os.Args[3]

	// signalServer := "https://meetupstation.com"
	// room.signalId = "secret room id"
	// room.meetingType = MeetingTypeHost

	interruptChannel := make(chan os.Signal, 1)
	signal.Notify(interruptChannel, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-interruptChannel
		err := mediaStream.closeRemoteStreams()
		if err != nil {
			fmt.Fprintf(os.Stderr, "close remote streams: %s\n", err)
		}
		err = mediaStream.closeLocalStreams()
		if err != nil {
			fmt.Fprintf(os.Stderr, "close local streams: %s\n", err)
		}
		os.Exit(0)
	}()

	var err error
	err = mediaStream.initializeLocalStreams(4000, 4002)
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize local streams: %s\n", err)
		return
	}
	go mediaStream.sendLocal(&room, MediaTypeAudio)
	go mediaStream.sendLocal(&room, MediaTypeVideo)

	err = mediaStream.initializeRemoteStreams(4004, 4006)
	if err != nil {
		fmt.Fprintf(os.Stderr, "initialize remote streams: %s\n", err)
		return
	}

	go mediaStream.receiveRemote(MediaTypeAudio)
	go mediaStream.receiveRemote(MediaTypeVideo)

	for {
		room.localSessionDescription = ""
		room.localCandidates = []string{}
		room.remoteSessionDescription = ""
		room.remoteCandidates = []string{}
		room.waitForAllICECandidates = nil

		mediaStream.remoteVideoTrack = nil
		mediaStream.remoteAudioTrack = nil

		peerConnectionId := room.nextPeerConnectionId

		err = room.initializePeerConnection()
		if err != nil {
			fmt.Fprintf(os.Stderr, "conn %d: %s\n", peerConnectionId, err)
			continue
		}

		for {
			if _, ok := room.peers[room.nextPeerConnectionId]; !ok {
				break
			}

			room.nextPeerConnectionId++
		}

		fmt.Fprintf(os.Stdout, "conn %d: setting up tracks and data handlers\n", peerConnectionId)

		err = room.prepareGuestAnswerOrHostOffer(&mediaStream, peerConnectionId, signalServer)
		if err != nil {
			fmt.Fprintf(os.Stderr, "conn %d: %s\n", peerConnectionId, err)
			time.Sleep(time.Second)
			continue
		}

		fmt.Fprintf(os.Stdout, "conn %d: signalling/waiting for ice connection\n", peerConnectionId)

		err = room.waitForIceConnected(peerConnectionId, signalServer)
		if err != nil {
			fmt.Fprintf(os.Stderr, "conn %d: %s\n", peerConnectionId, err)
			room.closePeer(peerConnectionId)
			time.Sleep(time.Second)
			continue
		}

		waitForDisconnected := func() {
			err = room.waitForIceDisconnected(peerConnectionId, signalServer)
			if err != nil {
				fmt.Fprintf(os.Stderr, "conn %d: %s\n", peerConnectionId, err)
			}
			room.closePeer(peerConnectionId)
		}

		switch room.meetingType {
		case MeetingTypeGuest:
			fmt.Fprintf(os.Stdout, "conn %d: signalling/waiting for ice disconnection\n", peerConnectionId)
			waitForDisconnected()
		case MeetingTypeHost:
			room.getPeer(peerConnectionId).signallingComplete = true
			fmt.Fprintf(os.Stdout, "conn %d: waiting for ice disconnection in a goroutine\n", peerConnectionId)
			go waitForDisconnected()
		}
	}
}
