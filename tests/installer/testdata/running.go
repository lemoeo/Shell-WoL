// A standalone executable for process-inspection tests. It creates no data lock.
package main

import (
	"fmt"
	"time"
)

func main() {
	fmt.Println("ready")
	time.Sleep(time.Minute)
}
