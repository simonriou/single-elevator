package main

import (
	"Driver-go/elevio"
	"Network-go/network/bcast"
	"Network-go/network/peers"
	"flag"
	"fmt"
	"sync"
	"time"
)

const numFloors = 4

var (
	elevatorOrders       []Order
	mutex_elevatorOrders sync.Mutex
)

var (
	posArray       [2*numFloors - 1]bool
	mutex_posArray sync.Mutex
)

var (
	ableToCloseDoors bool
	mutex_doors      sync.Mutex
)

var (
	role string
)

var mutex_d sync.Mutex

var lastDirForStopFunction elevio.MotorDirection

func lockMutexes(mutexes ...*sync.Mutex) {
	for _, m := range mutexes {
		m.Lock()
	}
}

func unlockMutexes(mutexes ...*sync.Mutex) {
	for _, m := range mutexes {
		m.Unlock()
	}
}

func turnOffLights(current_order Order, allFloors bool) {
	switch {
	case !allFloors:
		// Turn off the button lamp at the current floor
		if current_order.orderType == hall { // Hall button
			if current_order.direction == up { // Hall up
				elevio.SetButtonLamp(elevio.BT_HallUp, current_order.floor, false)
			} else { // Hall down
				elevio.SetButtonLamp(elevio.BT_HallDown, current_order.floor, false)
			}
		} else { // Cab button
			elevio.SetButtonLamp(elevio.BT_Cab, current_order.floor, false)
		}

	case allFloors:
		for f := 0; f < numFloors; f++ {
			for b := elevio.ButtonType(0); b < 3; b++ {
				elevio.SetButtonLamp(b, f, false)
			}
		}
	}
}

func main() {
	// Decide the port on which we are working
	port_val := flag.String("port", "", "The port of the elevator")
	role_val := flag.String("role", "", "The role of the elevator")
	flag.Parse()

	port := *port_val
	fmt.Printf("Working on address: %v\n", "localhost:"+port)
	role = *role_val
	fmt.Printf("Role passed: %v\n", role)

	peerUpdateCh := make(chan peers.PeerUpdate)
	peerTxEnable := make(chan bool)
	go peers.Transmitter(15647, role, peerTxEnable) // Creates a channel that broadcasts our role
	go peers.Receiver(15647, peerUpdateCh)          // Creates a channel that listens

	// We make channels for sending and receiving our custom data types
	helloTx := make(chan string)
	helloRx := make(chan string)

	go bcast.Transmitter(16569, helloTx)
	go bcast.Receiver(16569, helloRx)

	elevio.Init("localhost:"+port, numFloors)

	drv_buttons := make(chan elevio.ButtonEvent)
	drv_floors := make(chan int)
	drv_floors2 := make(chan int)
	drv_floors3 := make(chan int)
	drv_obstr := make(chan bool)
	drv_stop := make(chan bool)
	drv_newOrder := make(chan Order)
	drv_DirectionChange := make(chan elevio.MotorDirection)
	drv_finishedInitialization := make(chan bool)

	go elevio.PollButtons(drv_buttons)         // Starts checking for button updates
	go elevio.PollFloorSensor(drv_floors)      // Starts checking for floors updates
	go elevio.PollFloorSensor2(drv_floors2)    // Starts checking for floors updates (for tracking position)
	go elevio.PollFloorSensor(drv_floors3)     // Starts checking for floors updates (for safety measures)
	go elevio.PollObstructionSwitch(drv_obstr) // Starts checking for obstruction updates
	go elevio.PollStopButton(drv_stop)         // Starts checking for stop button presses

	var d elevio.MotorDirection = elevio.MD_Down

	// Section_START ---- Initialization

	go func() {
		elevio.SetMotorDirection(d)
		for {
			a := <-drv_floors
			if a == 0 {
				d = elevio.MD_Stop
				elevio.SetMotorDirection(d)
				break
			}
		}
		ableToCloseDoors = true
		turnOffLights(Order{0, -1, 0}, true)
		drv_finishedInitialization <- true
	}()

	<-drv_finishedInitialization

	fmt.Printf("Initialization finished\n")
	helloTx <- "Initialization finished.\n"

	// Section_END ---- Initialization

	go trackPosition(drv_floors2, drv_DirectionChange, &d) // Starts tracking the position of the elevator
	go attendToSpecificOrder(&d, drv_floors, drv_newOrder, drv_DirectionChange)

	if role == "Slave" {
		d = elevio.MD_Up
		elevio.SetMotorDirection(d)
	}

	for {
		select {
		case a := <-drv_buttons: // New button update
			// Gets a new order
			// Adds it to elevatorOrders and sorts

			time.Sleep(30 * time.Millisecond)

			elevio.SetButtonLamp(a.Button, a.Floor, true)

			lockMutexes(&mutex_elevatorOrders, &mutex_d, &mutex_posArray)

			switch {
			case a.Button == elevio.BT_HallUp:
				addOrder(a.Floor, up, hall)
			case a.Button == elevio.BT_HallDown:
				addOrder(a.Floor, down, hall)
			case a.Button == elevio.BT_Cab:
				addOrder(a.Floor, 0, cab)
			}

			fmt.Printf("\nAdded order, current direction is now: %v\n", d)
			fmt.Printf("Added order, elevatorOrders is now: %v\n", elevatorOrders)
			fmt.Printf("Added order, positionArray is now: %v\n", posArray)

			sortAllOrders(&elevatorOrders, d, posArray)
			// fmt.Printf("Sorted order, length of elevatorOrders is now: %d\n", len(elevatorOrders))

			first_element := elevatorOrders[0]

			// fmt.Printf("Sorted order\n")

			fmt.Printf("ElevatorOrders is now: %v\n", elevatorOrders)

			// Sending the first element of elevatorOrders through the drv_newOrder channel
			// We don't have to worry about the possibility of it being the same order that we are attending to
			// This is because we only set the current direction to the same as it was
			unlockMutexes(&mutex_elevatorOrders, &mutex_d, &mutex_posArray)

			drv_newOrder <- first_element

		case a := <-helloRx:
			fmt.Printf("Received: %#v\n", a)

		case a := <-drv_stop:
			switch {
			case a:
				// Rising edge, from unpressed to pressed
				fmt.Printf("Received rising edge from drv_stop\n")
				lockMutexes(&mutex_d)
				elevio.SetStopLamp(true)
				lastDirForStopFunction = d
				elevio.SetMotorDirection(elevio.MD_Stop)
				unlockMutexes(&mutex_d)

			case !a:
				// Falling edge, from pressed to unpressed
				fmt.Printf("Received falling edge from drv_stop\n")
				lockMutexes(&mutex_d)
				elevio.SetMotorDirection(lastDirForStopFunction)
				unlockMutexes(&mutex_d)

				elevio.SetStopLamp(false)
			}

		// case a := <-drv_floors3:
		// 	if a == 0 {
		// 		d = elevio.MD_Stop
		// 		elevio.SetMotorDirection(d)
		// 	}

		case a := <-drv_obstr: // Obstruction switch pressed (meaning doors are opened)
			// Unable to close the doors until obstruction switch is released
			if a { // If it is on
				lockMutexes(&mutex_doors)
				ableToCloseDoors = false
				unlockMutexes(&mutex_doors)
				fmt.Print("Obstruction on\n")
			} else { // If it is off
				lockMutexes(&mutex_doors)
				ableToCloseDoors = true
				unlockMutexes(&mutex_doors)
				fmt.Print("Obstruction off\n")
			}
		}

	}
}
