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
			fmt.Fprintf(os.Stderr, "failed to close remote streams\n")
		}
		os.Exit(0)
	}()

	go mediaStream.sendLocal(&room, MediaTypeAudio, 4000)
	go mediaStream.sendLocal(&room, MediaTypeVideo, 4002)

	room.signalAccessKey = ""

	var err error
	err = mediaStream.initializeRemoteStreams()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to initialize remote streams\n")
		return
	}

	go mediaStream.receiveRemote(MediaTypeAudio)
	go mediaStream.receiveRemote(MediaTypeVideo)

	for {
		room.localSessionDescription = ""
		room.localCandidates = []string{}
		room.remoteSessionDescription = ""
		room.remoteCandidates = []string{}
		room.signallingComplete = false
		room.waitForAllICECandidates = nil

		mediaStream.remoteVideoTrack = nil
		mediaStream.remoteAudioTrack = nil

		peerConnectionId := room.nextPeerConnectionId
		fmt.Fprintf(os.Stdout, "conn %d, starting in a second...\n", peerConnectionId)
		time.Sleep(time.Second)

		err = room.initializePeerConnection()
		if err != nil {
			fmt.Fprintf(os.Stderr, "conn %d: %s\n", peerConnectionId, err)
			continue
		}
		room.nextPeerConnectionId++

		fmt.Fprintf(os.Stdout, "conn %d: setting up tracks and data handlers\n", peerConnectionId)

		err = room.prepareGuestAnswerOrHostOffer(&mediaStream, peerConnectionId, signalServer)
		if err != nil {
			fmt.Fprintf(os.Stderr, "conn %d: %s\n", peerConnectionId, err)
			continue
		}

		fmt.Fprintf(os.Stdout, "conn %d: waiting for ice connection\n", peerConnectionId)

		err = room.waitForIceConnected(peerConnectionId, signalServer)
		if err != nil {
			fmt.Fprintf(os.Stderr, "conn %d: %s\n", peerConnectionId, err)
		} else if room.meetingType == MeetingTypeGuest {
			fmt.Fprintf(os.Stdout, "conn %d: waiting for ice disconnection\n", peerConnectionId)
			err = room.waitForIceDisconnected(peerConnectionId, signalServer)
			if err != nil {
				fmt.Fprintf(os.Stderr, "conn %d: %s\n", peerConnectionId, err)
			}
		}
	}
}
