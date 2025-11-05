package main

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const userAgent = "minimalwave (https://github.com/miku/minimalwave)"

func main() {
	userCacheDir, err := os.UserCacheDir()
	if err != nil {
		log.Fatal(err)
	}
	cacheDir := filepath.Join(userCacheDir, "minimalwave")
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		log.Fatal(err)
	}
	if err := cleanupTempFiles(cacheDir); err != nil {
		log.Printf("warning: failed to clean up temp files: %v", err)
	}
	var (
		identifier = identifiers[time.Now().UnixNano()%int64(len(identifiers))]
		hasher     = md5.New()
	)
	hasher.Write([]byte(identifier))
	var (
		hash      = hex.EncodeToString(hasher.Sum(nil))
		filename  = fmt.Sprintf("%s.mp3", hash)
		cachePath = filepath.Join(cacheDir, filename)
	)
	if err := downloadOrUseCached(identifier, cachePath); err != nil {
		log.Fatal(err)
	}
	player, args, err := findPlayer(cachePath)
	if err != nil {
		log.Fatal(err)
	}
	cmd := exec.Command(player, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Stdin = os.Stdin
	if err := cmd.Start(); err != nil {
		log.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			if exitError.ExitCode() == 130 { // SIGINT
				return
			}
		}
		log.Fatal(err)
	}
}

func downloadOrUseCached(identifier, cachePath string) error {
	if _, err := os.Stat(cachePath); err == nil {
		return nil
	}
	if len(identifier) != 23 {
		return fmt.Errorf("unexpected id: %v", identifier)
	}
	url := fmt.Sprintf("https://archive.org/download/%s/%s.mp3", identifier, identifier[4:])

	stopAnimation := make(chan bool)
	go animateConnecting(stopAnimation)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	stopAnimation <- true
	fmt.Print("\r\033[K")

	if err != nil {
		return fmt.Errorf("failed to download: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download (%s) failed with status: %d", url, resp.StatusCode)
	}

	cacheDir := filepath.Dir(cachePath)
	tempFile, err := os.CreateTemp(cacheDir, "minimalwave-*.mp3.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temporary file: %v", err)
	}
	tempPath := tempFile.Name()

	defer os.Remove(tempPath) // Always clean up temp file

	// Start player immediately
	player, args, err := findPlayer(tempPath)
	if err != nil {
		tempFile.Close()
		return err
	}
	cmd := exec.Command(player, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.Stdin = os.Stdin

	time.Sleep(100 * time.Millisecond)

	if err := cmd.Start(); err != nil {
		tempFile.Close()
		return fmt.Errorf("failed to start player: %v", err)
	}

	// Download while player is running
	totalSize := resp.ContentLength
	progressReader := &progressReader{
		reader:     resp.Body,
		total:      totalSize,
		onProgress: createColorBlockProgress(totalSize),
	}

	_, err = io.Copy(tempFile, progressReader)
	fmt.Print("\r\033[K")

	if err != nil {
		tempFile.Close()
		cmd.Process.Kill()
		return fmt.Errorf("failed to save to cache: %v", err)
	}

	if err := tempFile.Close(); err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("failed to close temporary file: %v", err)
	}

	// Download complete - copy to cache while player continues using temp file
	if err := copyFile(tempPath, cachePath); err != nil {
		// Don't kill player, just log the cache failure
		log.Printf("warning: failed to cache file: %v", err)
	}

	// Wait for player to finish
	if err := cmd.Wait(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			if exitError.ExitCode() == 130 {
				return nil
			}
		}
		return err
	}

	return nil
}

// progressReader wraps an io.Reader to track read progress
type progressReader struct {
	reader     io.Reader
	total      int64
	current    int64
	onProgress func(current, total int64)
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.reader.Read(p)
	pr.current += int64(n)
	if pr.onProgress != nil {
		pr.onProgress(pr.current, pr.total)
	}
	return n, err
}

// animateConnecting displays a single block cycling through colors
func animateConnecting(stop chan bool) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	colors := []int{
		196, 202, 208, 214, 220, 226, // reds to yellows
		46, 47, 48, 49, 50, 51, // greens
		39, 45, 81, 87, 123, // cyans to blues
		129, 135, 141, 177, 183, // purples
		201, 207, 213, 219, 225, // pinks to light colors
	}

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			color := colors[rng.Intn(len(colors))]
			// Move cursor back 1 position, draw colored block, move cursor back 1 position
			fmt.Printf("\033[1D\033[48;5;%dm \033[0m\033[1D", color)
		}
	}
}

// createColorBlockProgress returns a progress callback that displays colorful blocks
func createColorBlockProgress(total int64) func(current, total int64) {
	const blockCount = 64
	var (
		lastBlocks = 0
		rng        = rand.New(rand.NewSource(time.Now().UnixNano()))
	)

	// ANSI 256-color palette - vibrant colors
	colors := []int{
		196, 202, 208, 214, 220, 226, // reds to yellows
		46, 47, 48, 49, 50, 51, // greens
		39, 45, 81, 87, 123, // cyans to blues
		129, 135, 141, 177, 183, // purples
		201, 207, 213, 219, 225, // pinks to light colors
	}

	return func(current, total int64) {
		if total <= 0 {
			return
		}

		progress := float64(current) / float64(total)
		blocks := int(progress * blockCount)

		if blocks > lastBlocks {
			for i := lastBlocks; i < blocks; i++ {
				color := colors[rng.Intn(len(colors))]
				fmt.Printf("\033[48;5;%dm \033[0m", color)
			}
			lastBlocks = blocks
		}
	}
}

func findPlayer(filePath string) (string, []string, error) {
	players := []struct {
		name string
		args []string
	}{
		{"afplay", []string{filePath}}, // macos
		{"cvlc", []string{"-q", filePath}},
		{"vlc", []string{"-q", "--intf", "dummy", filePath}},
		{"mpg123", []string{filePath}},
		{"ffplay", []string{"-autoexit", filePath}},
		{"mplayer", []string{filePath}},
		{"omxplayer", []string{filePath}},
	}
	for _, player := range players {
		if _, err := exec.LookPath(player.name); err == nil {
			return player.name, player.args, nil
		}
	}
	return "", nil, fmt.Errorf("no suitable player found")
}

// cleanupTempFiles removes any .tmp files left from interrupted downloads
func cleanupTempFiles(cacheDir string) error {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".tmp" {
			path := filepath.Join(cacheDir, entry.Name())
			if err := os.Remove(path); err != nil {
				log.Printf("warning: failed to remove temp file %s: %v", path, err)
			}
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, sourceFile)
	return err
}

// identifiers can be mapped to download urls, https://archive.org/download/evr_1280-23176-20101128/1280-23176-20101128.mp3
var identifiers = []string{
	"evr_1280-10731-20091213",
	"evr_1280-10733-20091227",
	"evr_1280-10735-20100110",
	"evr_1280-10737-20100124",
	"evr_1280-10739-20100207",
	"evr_1280-10741-20100221",
	"evr_1280-10743-20100307",
	"evr_1280-11191-20100321",
	"evr_1280-12911-20090726",
	"evr_1280-12913-20090809",
	"evr_1280-12915-20090823",
	"evr_1280-12917-20090906",
	"evr_1280-12919-20090920",
	"evr_1280-12921-20091004",
	"evr_1280-12923-20091018",
	"evr_1280-12925-20091101",
	"evr_1280-14172-20091115",
	"evr_1280-14174-20091129",
	"evr_1280-22294-20101031",
	"evr_1280-22353-20100822",
	"evr_1280-22385-20101003",
	"evr_1280-22412-20110109",
	"evr_1280-22467-20101212",
	"evr_1280-22472-20100613",
	"evr_1280-22476-20110220",
	"evr_1280-22528-20110320",
	"evr_1280-22534-20100404",
	"evr_1280-22549-20110403",
	"evr_1280-22570-20100516",
	"evr_1280-22622-20100905",
	"evr_1280-22636-20101114",
	"evr_1280-22695-20100725",
	"evr_1280-22724-20110123",
	"evr_1280-22747-20100627",
	"evr_1280-22798-20100418",
	"evr_1280-22824-20101017",
	"evr_1280-22867-20100530",
	"evr_1280-22996-20100808",
	"evr_1280-23020-20101226",
	"evr_1280-23095-20110306",
	"evr_1280-23102-20100502",
	"evr_1280-23134-20110417",
	"evr_1280-23176-20101128",
	"evr_1280-23232-20100919",
	"evr_1280-23284-20100711",
	"evr_1280-23331-20110206",
	"evr_1280-39497-20110508",
	"evr_1280-39653-20110522",
	"evr_1280-39808-20110605",
	"evr_1280-39962-20110619",
	"evr_1280-40116-20110703",
	"evr_1280-40270-20110717",
	"evr_1280-40423-20110731",
	"evr_1280-40571-20110814",
	"evr_1280-40719-20110828",
	"evr_1280-40865-20110911",
	"evr_1280-41011-20110925",
	"evr_1280-41157-20111009",
	"evr_1280-41303-20111023",
	"evr_1280-41449-20111106",
	"evr_1280-41595-20111120",
	"evr_1280-41741-20111204",
	"evr_1280-41887-20111218",
	"evr_1280-42033-20120101",
	"evr_1280-42179-20120115",
	"evr_1280-42325-20120129",
	"evr_1280-42471-20120212",
	"evr_1280-42617-20120226",
	"evr_1280-42763-20120311",
	"evr_1280-42909-20120325",
	"evr_1280-43055-20120408",
	"evr_1280-43201-20120422",
	"evr_1280-43347-20120506",
	"evr_1280-43493-20120520",
	"evr_1280-43639-20120603",
	"evr_1280-43785-20120617",
	"evr_1280-43931-20120701",
	"evr_1280-44077-20120715",
	"evr_1280-44223-20120729",
	"evr_1280-44369-20120812",
	"evr_1280-44515-20120826",
	"evr_1280-44661-20120909",
	"evr_1280-44807-20120923",
	"evr_1280-44951-20121007",
	"evr_1280-45095-20121021",
	"evr_1280-45239-20121104",
	"evr_1280-45383-20121118",
	"evr_1280-45527-20121202",
	"evr_1280-45671-20121216",
	"evr_1280-45815-20121230",
	"evr_1280-45959-20130113",
	"evr_1280-46103-20130127",
	"evr_1280-46247-20130210",
	"evr_1280-46391-20130224",
	"evr_1280-46535-20130310",
	"evr_1280-46679-20130324",
	"evr_1280-46823-20130407",
	"evr_1280-46967-20130421",
	"evr_1280-47111-20130505",
	"evr_1280-47255-20130519",
	"evr_1280-47399-20130602",
	"evr_1280-47543-20130616",
	"evr_1280-47687-20130630",
	"evr_1280-47831-20130714",
	"evr_1280-47975-20130728",
	"evr_1280-48119-20130811",
	"evr_1280-48263-20130825",
	"evr_1280-48407-20130908",
	"evr_1280-48551-20130922",
	"evr_1280-48695-20131006",
	"evr_1280-48839-20131020",
	"evr_1280-48983-20131103",
	"evr_1280-49127-20131117",
	"evr_1280-49271-20131201",
	"evr_1280-49415-20131215",
	"evr_1280-49559-20131229",
	"evr_1280-49703-20140112",
	"evr_1280-49847-20140126",
	"evr_1280-50135-20140223",
	"evr_1280-50279-20140309",
	"evr_1280-50423-20140323",
	"evr_1280-50567-20140406",
	"evr_1280-50711-20140420",
	"evr_1280-50855-20140504",
	"evr_1280-50999-20140518",
}
