package main

import (
	"context"
	"errors"
)

// App holds Wails application state.
type App struct {
	ctx context.Context
}

// NewApp creates a new App application struct.
func NewApp() *App {
	return &App{}
}

// startup is called when the app starts. The context is saved
// so we can call the runtime methods.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

// ConfigStub returns a placeholder host list; replaced by config task.
func (a *App) ConfigStub() []string {
	return []string{"prod-db", "staging"}
}

// ToggleTunnelStub toggles a tunnel; replaced by forward task.
func (a *App) ToggleTunnelStub(id string) error {
	return errors.New("not implemented yet")
}
