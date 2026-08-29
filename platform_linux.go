//go:build !windows

package main

import (
	"os/exec"
)

func platformRun(url string) {
	// Linux/macOS: abre o navegador e fica rodando
	cmd := exec.Command("xdg-open", url)
	cmd.Start()

	// Mantem rodando ate que o endpoint /api/shutdown seja chamado
	select {}
}
