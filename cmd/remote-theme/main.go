// Export the terminal palette for the independently deployed phone app.
package main

import (
	"fmt"
	"os"
	"partyline.sh/partyline/internal/theme"
)

func main() {
	t := theme.Default()
	css := fmt.Sprintf(":root{--ground:%s;--surface:%s;--text:%s;--muted:%s;--focus:%s;--border:%s}\n", t.Ground, t.Surface, t.Text, t.Muted, t.Focus, t.Border)
	if err := os.WriteFile("relay/public/theme.css", []byte(css), 0644); err != nil {
		panic(err)
	}
}
