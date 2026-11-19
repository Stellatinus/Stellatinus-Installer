// Command demoapp is the program inside the test installer's fake app, so that
// a shortcut to it opens something. It prints what it is, where it was
// installed and the settings the installer wrote for it, then waits for Enter.
// It does nothing else: it reads one file and changes nothing.
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	fmt.Println("Stellatinus Demo")
	fmt.Println()
	fmt.Println("This is a fake app that comes with the test installer. It does nothing.")
	fmt.Println("If you can read this, the installer put the program in place and the")
	fmt.Println("shortcut (or the file you opened) really starts it.")
	fmt.Println()
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(filepath.Dir(exe)) // the program is in <install folder>/bin
		fmt.Println("Installed in:", dir)
		if b, err := os.ReadFile(filepath.Join(dir, "config.json")); err == nil {
			fmt.Printf("Settings the installer wrote (config.json):\n%s", b)
		} else {
			fmt.Println("No config.json next to it:", err)
		}
	}
	fmt.Println()
	fmt.Print("Press Enter to close. ")
	_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
}
