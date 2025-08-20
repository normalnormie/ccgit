package main

import (
    "bufio"
    "context"
    "encoding/json"
    "fmt"
    "io"
    "log"
    "os"
    "os/exec"
    "os/signal"
    "path/filepath"
    "strings"
    "sync"
    "syscall"
    "time"
)

var (
    config             *Config
    logger             *log.Logger
    waitTimer          *time.Timer
    waitMutex          sync.Mutex
    pollInterval       = 2 * time.Second
)

type MessageContent struct {
    Type     string `json:"type"`
    Thinking string `json:"thinking"`
}

type Message struct {
    Role    string          `json:"role"`
    Content json.RawMessage `json:"content"`
}

type ThinkingEntry struct {
    ParentUUID string    `json:"parentUuid"`
    Type       string    `json:"type"`
    Message    Message   `json:"message"`
    Timestamp  string    `json:"timestamp"`
}

func (te *ThinkingEntry) GetThinkingContent() string {
    // First try to unmarshal as array (assistant messages)
    var contentArray []MessageContent
    if err := json.Unmarshal(te.Message.Content, &contentArray); err == nil {
        for _, content := range contentArray {
            if content.Type == "thinking" {
                return content.Thinking
            }
        }
        return ""
    }
    
    // If array unmarshal fails, it's probably a string (user messages)
    // We don't extract thinking from user messages
    return ""
}

type Config struct {
    LastProcessedUUID   string   `json:"lastProcessedUUID"`
    Simulate            bool     `json:"simulate"`
    Verbose             bool     `json:"verbose"`
    WaitingForThinking  bool     `json:"waitingForThinking"`
    AccumulatedThinking []string `json:"accumulatedThinking"`
    WaitingTargetUUID   string   `json:"waitingTargetUUID"`
    LastFilePosition    int64    `json:"lastFilePosition"`
    MonitoredFile       string   `json:"monitoredFile"`
    MainBranch          string   `json:"mainbranch"`
    QuitOnConflict      bool     `json:"quitonconflict"`
    ProjectsDir         string   `json:"projectsdir"`
}

func loadConfig() (*Config, error) {
    config := &Config{
        Simulate:       false,
        Verbose:        false,
        MainBranch:     "master",
        QuitOnConflict: false,
        ProjectsDir:    "~/.claude/projects/",
    }
    
    if _, err := os.Stat("ccgit.conf"); os.IsNotExist(err) {
        return config, saveConfig(config)
    }
    
    data, err := os.ReadFile("ccgit.conf")
    if err != nil {
        return config, err
    }
    
    if err := json.Unmarshal(data, config); err != nil {
        return config, err
    }
    
    return config, nil
}

func saveConfig(config *Config) error {
    data, err := json.MarshalIndent(config, "", "  ")
    if err != nil {
        return err
    }
    return os.WriteFile("ccgit.conf", data, 0644)
}


func ensureGitignore() error {
    gitignorePath := ".gitignore"
    entries := []string{"ccgit.conf", "ccgit"}
    
    // Check if .gitignore exists
    if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
        // Create .gitignore with our entries
        logger.Printf("Creating .gitignore with ccgit entries")
        content := strings.Join(entries, "\n") + "\n"
        return os.WriteFile(gitignorePath, []byte(content), 0644)
    }
    
    // Read existing .gitignore
    content, err := os.ReadFile(gitignorePath)
    if err != nil {
        return fmt.Errorf("failed to read .gitignore: %v", err)
    }
    
    contentStr := string(content)
    lines := strings.Split(contentStr, "\n")
    
    // Check which entries are missing
    existingEntries := make(map[string]bool)
    for _, line := range lines {
        existingEntries[strings.TrimSpace(line)] = true
    }
    
    var missingEntries []string
    for _, entry := range entries {
        if !existingEntries[entry] {
            missingEntries = append(missingEntries, entry)
        } else {
            logger.Printf("%s already in .gitignore", entry)
        }
    }
    
    // Add missing entries
    if len(missingEntries) > 0 {
        logger.Printf("Adding to .gitignore: %s", strings.Join(missingEntries, ", "))
        if !strings.HasSuffix(contentStr, "\n") && len(contentStr) > 0 {
            contentStr += "\n"
        }
        contentStr += strings.Join(missingEntries, "\n") + "\n"
        return os.WriteFile(gitignorePath, []byte(contentStr), 0644)
    }
    
    return nil
}

func main() {
    logger = log.New(os.Stdout, "ccgit: ", log.LstdFlags|log.Lshortfile)
    
    var err error
    config, err = loadConfig()
    if err != nil {
        logger.Fatalf("Failed to load config: %v", err)
    }

    // Ensure ccgit.conf is in .gitignore
    if err := ensureGitignore(); err != nil {
        logger.Printf("Warning: Failed to update .gitignore: %v", err)
    }

    wd, err := os.Getwd()
    if err != nil {
        logger.Fatalf("Failed to get working directory: %v", err)
    }
    logger.Printf("Starting in working directory: %s", wd)

    // Ensure initial commit exists
    cmd := exec.Command("git", "rev-list", "-n", "1", "HEAD")
    if err := cmd.Run(); err != nil {
        logger.Printf("No commits in repository, creating initial empty commit")
        if err := gitCmd("commit", "--allow-empty", "-m", "Initial commit"); err != nil {
            logger.Fatalf("Failed to create initial commit: %v", err)
        }
    }

    logger.Printf("Starting continuous JSONL monitoring (poll interval: %v)", pollInterval)
    
    // Create context for graceful shutdown
    ctx, cancel := context.WithCancel(context.Background())
    
    // Set up signal handling for graceful shutdown
    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
    
    go func() {
        sig := <-sigChan
        logger.Printf("Received signal %v, initiating graceful shutdown...", sig)
        cancel()
    }()
    
    // Start continuous monitoring loop
    ticker := time.NewTicker(pollInterval)
    defer ticker.Stop()
    
    for {
        select {
        case <-ctx.Done():
            logger.Printf("Context cancelled, performing graceful shutdown...")
            gracefulShutdown()
            logger.Printf("Graceful shutdown complete, exiting")
            return
        case <-ticker.C:
            monitorJSONL()
        }
    }
}


func monitorJSONL() {
    home := os.Getenv("HOME")
    if home == "" {
        logger.Println("HOME environment variable not set")
        return
    }
    wd, err := os.Getwd()
    if err != nil {
        logger.Printf("Failed to get working directory: %v", err)
        return
    }
    projectName := strings.ReplaceAll(wd, "/", "-")
    
    // Use configurable projects directory, expanding tilde
    projectsDir := config.ProjectsDir
    if strings.HasPrefix(projectsDir, "~/") {
        projectsDir = filepath.Join(home, projectsDir[2:])
    }
    
    claudeProjDir := filepath.Join(projectsDir, projectName)
    
    // Add fallback logic - only check ~/.config/claude/projects/ if using the default
    if config.ProjectsDir == "~/.claude/projects/" {
        if _, err := os.Stat(claudeProjDir); os.IsNotExist(err) {
            fallbackDir := filepath.Join(home, ".config/claude/projects", projectName)
            if _, fallbackErr := os.Stat(fallbackDir); fallbackErr == nil {
                logger.Printf("Using fallback directory: %s", fallbackDir)
                claudeProjDir = fallbackDir
            }
        }
    }
    
    // Find the latest JSONL file
    latestFile := findLatestJSONLFile(claudeProjDir)
    if latestFile == "" {
        if config.Verbose {
            logger.Printf("No .jsonl file found in Claude project directory: %s", claudeProjDir)
        }
        return
    }
    
    // Check if this is a different file than we were monitoring
    if config.MonitoredFile != latestFile {
        logger.Printf("Switching to monitor file: %s", latestFile)
        
        // Before switching, commit any accumulated thinking and merge current branch
        if config.MonitoredFile != "" {
            if err := commitAndMergeBeforeSwitching(); err != nil {
                if config.QuitOnConflict {
                    logger.Fatalf("Failed to commit and merge before switching: %v", err)
                } else {
                    logger.Printf("Warning: Failed to commit and merge before switching: %v", err)
                }
            }
        }
        
        config.MonitoredFile = latestFile
        
        // Set position to end of file to only pick up new thinking from this point forward
        if fileInfo, err := os.Stat(latestFile); err == nil {
            config.LastFilePosition = fileInfo.Size()
            logger.Printf("Set LastFilePosition to end of file: %d bytes", config.LastFilePosition)
        } else {
            config.LastFilePosition = 0
            logger.Printf("Failed to get file size, starting from beginning: %v", err)
        }
        
        config.LastProcessedUUID = ""
        config.WaitingForThinking = false
        config.AccumulatedThinking = nil
        config.WaitingTargetUUID = ""
        if err := saveConfig(config); err != nil {
            logger.Printf("Failed to save config: %v", err)
        }
    }
    
    // Check if file has new content
    fileInfo, err := os.Stat(latestFile)
    if err != nil {
        logger.Printf("Failed to stat file %s: %v", latestFile, err)
        return
    }
    
    currentSize := fileInfo.Size()
    if currentSize <= config.LastFilePosition {
        // No new content
        if config.Verbose {
            logger.Printf("No new content in %s (size: %d, last position: %d)", latestFile, currentSize, config.LastFilePosition)
        }
        return
    }
    
    // Read new content from the file
    newEntries, newPosition, err := readNewJSONLEntries(latestFile, config.LastFilePosition)
    if err != nil {
        logger.Printf("Failed to read new entries from %s: %v", latestFile, err)
        return
    }
    
    if len(newEntries) > 0 {
        logger.Printf("Found %d new entries in %s", len(newEntries), latestFile)
        processNewEntries(newEntries, latestFile)
        config.LastFilePosition = newPosition
        if err := saveConfig(config); err != nil {
            logger.Printf("Failed to save config: %v", err)
        }
    }
}

func findLatestJSONLFile(claudeProjDir string) string {
    var latestFile string
    var latestModTime time.Time
    
    err := filepath.Walk(claudeProjDir, func(path string, info os.FileInfo, err error) error {
        if err != nil {
            if config.Verbose {
                logger.Printf("Error walking Claude project directory %s: %v", path, err)
            }
            return nil // Continue walking
        }
        if !info.IsDir() && strings.HasSuffix(info.Name(), ".jsonl") && info.ModTime().After(latestModTime) {
            latestModTime = info.ModTime()
            latestFile = path
            if config.Verbose {
                logger.Printf("Found jsonl file: %s (mod time: %s)", path, latestModTime)
            }
        }
        return nil
    })
    
    if err != nil {
        logger.Printf("Failed to walk Claude project directory: %v", err)
    }
    
    return latestFile
}

func readNewJSONLEntries(filePath string, fromPosition int64) ([]ThinkingEntry, int64, error) {
    file, err := os.Open(filePath)
    if err != nil {
        return nil, 0, err
    }
    defer file.Close()
    
    // Seek to the last read position
    _, err = file.Seek(fromPosition, io.SeekStart)
    if err != nil {
        return nil, 0, err
    }
    
    var entries []ThinkingEntry
    scanner := bufio.NewScanner(file)
    const maxBufferSize = 100 * 1024 * 1024 // 100MB buffer size
    buf := make([]byte, 0, maxBufferSize)
    scanner.Buffer(buf, maxBufferSize)
    
    currentPosition := fromPosition
    lineNum := 0
    
    for scanner.Scan() {
        line := scanner.Text()
        lineNum++
        lineBytes := len(scanner.Bytes()) + 1 // +1 for newline
        
        if config.Verbose {
            logger.Printf("Reading new line %d: %s", lineNum, line)
        }
        
        if strings.TrimSpace(line) == "" {
            currentPosition += int64(lineBytes)
            continue
        }
        
        var entry ThinkingEntry
        if err := json.Unmarshal([]byte(line), &entry); err != nil {
            logger.Printf("Failed to unmarshal JSON at line %d: %v", lineNum, err)
            currentPosition += int64(lineBytes)
            continue
        }
        
        if entry.Type != "assistant" {
            if config.Verbose {
                logger.Printf("Skipping non-assistant entry at line %d: Type=%s", lineNum, entry.Type)
            }
            currentPosition += int64(lineBytes)
            continue
        }
        
        thinkingContent := entry.GetThinkingContent()
        if thinkingContent == "" {
            if config.Verbose {
                logger.Printf("Skipping assistant entry with no thinking content at line %d", lineNum)
            }
            currentPosition += int64(lineBytes)
            continue
        }
        
        entries = append(entries, entry)
        currentPosition += int64(lineBytes)
        logger.Printf("Found new thinking entry: UUID=%s, Thinking=%s", entry.ParentUUID, thinkingContent)
    }
    
    if err := scanner.Err(); err != nil {
        return nil, currentPosition, err
    }
    
    return entries, currentPosition, nil
}

func processNewEntries(entries []ThinkingEntry, filePath string) {
    if len(entries) == 0 {
        return
    }
    
    // Extract branch name from file path
    branchName := strings.TrimSuffix(filepath.Base(filePath), ".jsonl")
    if config.Verbose {
        logger.Printf("Processing entries for branch: %s", branchName)
    }
    
    // Ensure we're on the correct branch
    ensureBranch(branchName)
    
    // Extract thinking content from all new entries
    var allNewThinking []string
    for _, entry := range entries {
        thinkingContent := entry.GetThinkingContent()
        if thinkingContent != "" {
            allNewThinking = append(allNewThinking, thinkingContent)
        }
    }
    
    if len(allNewThinking) == 0 {
        logger.Printf("No thinking content found in new entries")
        return
    }
    
    // Process thinking using existing wait logic
    processThinkingEntries(entries, allNewThinking)
}

func ensureBranch(branchName string) {
    if !gitBranchExists(branchName) {
        logger.Printf("Creating new branch: %s", branchName)
        if err := gitCmd("checkout", "-b", branchName); err != nil {
            logger.Printf("Failed to create branch %s: %v", branchName, err)
            return
        }
    } else {
        logger.Printf("Switching to existing branch: %s", branchName)
        if err := gitCmd("checkout", branchName); err != nil {
            logger.Printf("Failed to switch to branch %s: %v", branchName, err)
            return
        }
    }
}

func processThinkingEntries(entries []ThinkingEntry, allNewThinking []string) {
    waitMutex.Lock()
    defer waitMutex.Unlock()
    
    logger.Printf("Processing thinking: WaitingForThinking=%t, found %d new entries, accumulated %d entries", 
        config.WaitingForThinking, len(allNewThinking), len(config.AccumulatedThinking))
    
    if !config.WaitingForThinking {
        // Not waiting - start waiting if we have new thinking
        if len(allNewThinking) > 0 {
            logger.Printf("Starting wait state with %d thinking entries", len(allNewThinking))
            config.WaitingForThinking = true
            config.AccumulatedThinking = allNewThinking
            config.WaitingTargetUUID = entries[len(entries)-1].ParentUUID
            
            // Start 30-second timer
            waitTimer = time.AfterFunc(30*time.Second, func() {
                logger.Printf("30-second timeout reached, committing thinking")
                commitAccumulatedThinking()
            })
            
            if err := saveConfig(config); err != nil {
                logger.Printf("Failed to save config: %v", err)
            } else {
                logger.Printf("Config saved with waiting state")
            }
            logger.Printf("Started waiting for one more thinking entry (30s timeout), found %d entries", len(allNewThinking))
        }
    } else {
        // Currently waiting - check if we got more thinking
        logger.Printf("Currently waiting, comparing %d new vs %d accumulated", len(allNewThinking), len(config.AccumulatedThinking))
        totalThinking := len(config.AccumulatedThinking) + len(allNewThinking)
        if totalThinking > len(config.AccumulatedThinking) {
            // We have additional thinking - commit accumulated + exactly one additional
            logger.Printf("Found additional thinking, will commit first additional only")
            
            // Commit accumulated + first additional thinking only
            thinkingToCommit := make([]string, len(config.AccumulatedThinking) + 1)
            copy(thinkingToCommit, config.AccumulatedThinking)
            thinkingToCommit[len(config.AccumulatedThinking)] = allNewThinking[0]
            
            targetUUID := entries[0].ParentUUID
            
            // Update config for commit
            config.AccumulatedThinking = thinkingToCommit
            config.WaitingTargetUUID = targetUUID
            
            logger.Printf("Committing %d thinking entries, target UUID: %s", 
                len(thinkingToCommit), targetUUID)
            
            // Cancel timer
            if waitTimer != nil {
                logger.Printf("Canceling 30s timer")
                waitTimer.Stop()
            }
            
            commitAccumulatedThinkingLocked()
        } else {
            logger.Printf("No additional thinking beyond accumulated, continuing to wait")
        }
    }
    
    // Save updated config
    if err := saveConfig(config); err != nil {
        logger.Printf("Failed to save config: %v", err)
    }
}

// Legacy ccgit function - kept for compatibility but no longer used
func ccgit() {
    // This function is no longer used - monitorJSONL() handles the logic now
    logger.Printf("Legacy ccgit() function called - this should not happen")
}

func gitBranchExists(branch string) bool {
    wd, err := os.Getwd()
    if err != nil {
        logger.Printf("Failed to get working directory: %v", err)
    }
    if config.Verbose {
        logger.Printf("Checking branch %s in directory: %s", branch, wd)
    }

    cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
    output, err := cmd.CombinedOutput()
    if err == nil && strings.TrimSpace(string(output)) == branch {
        if config.Verbose {
            logger.Printf("Branch %s is current branch (via git rev-parse --abbrev-ref HEAD)", branch)
        }
        return true
    }
    if config.Verbose {
        logger.Printf("Branch %s not current branch: %v, output: %s", branch, err, string(output))
    }

    cmd = exec.Command("git", "branch", "--list", branch)
    output, err = cmd.CombinedOutput()
    if err == nil && strings.Contains(string(output), branch) {
        if config.Verbose {
            logger.Printf("Branch %s exists (via git branch --list), output: %s", branch, string(output))
        }
        return true
    }
    if config.Verbose {
        logger.Printf("Branch %s not found via git branch --list: %v, output: %s", branch, err, string(output))
    }

    cmd = exec.Command("git", "branch", "--all")
    output, err = cmd.CombinedOutput()
    if config.Verbose {
        if err != nil {
            logger.Printf("Failed to list all branches: %v, output: %s", err, string(output))
        } else {
            logger.Printf("All branches: %s", string(output))
        }
    }

    return false
}

func gitCmd(args ...string) error {
    if config.Simulate {
        logger.Printf("Simulating: git %s", strings.Join(args, " "))
        return nil
    }
    logger.Printf("Executing: git %s", strings.Join(args, " "))
    cmd := exec.Command("git", args...)
    output, err := cmd.CombinedOutput()
    if err != nil {
        logger.Printf("Git command failed: %v, output: %s", err, string(output))
        return fmt.Errorf("git %s: %v, output: %s", strings.Join(args, " "), err, string(output))
    }
    if config.Verbose {
        logger.Printf("Git command output: %s", string(output))
    }
    return nil
}

func gitCommit(msg string) error {
    logger.Printf("Staging all changes")
    if err := gitCmd("add", "-A"); err != nil {
        return err
    }
    logger.Printf("Committing with message: %s", msg)
    return gitCmd("commit", "-m", msg)
}

func commitAccumulatedThinking() {
    waitMutex.Lock()
    defer waitMutex.Unlock()
    commitAccumulatedThinkingLocked()
}

// Internal function that assumes mutex is already held
func commitAccumulatedThinkingLocked() {
    logger.Printf("commitAccumulatedThinking called: WaitingForThinking=%t, AccumulatedThinking count=%d", config.WaitingForThinking, len(config.AccumulatedThinking))
    
    if !config.WaitingForThinking {
        logger.Printf("Not waiting for thinking, returning early")
        return // Already committed
    }
    
    if len(config.AccumulatedThinking) > 0 {
        combinedThinking := strings.Join(config.AccumulatedThinking, "\n\n---\n\n")
        logger.Printf("Committing accumulated thinking from %d entries", len(config.AccumulatedThinking))
        if err := gitCommit(combinedThinking); err != nil {
            // Check if the error is just "nothing to commit" - if so, continue
            if strings.Contains(err.Error(), "nothing to commit") {
                logger.Printf("Nothing to commit (working tree clean), updating config anyway")
            } else {
                logger.Printf("Failed to commit thinking: %v", err)
                return
            }
        }
        
        // Update LastProcessedUUID to the target UUID
        config.LastProcessedUUID = config.WaitingTargetUUID
        logger.Printf("Updated LastProcessedUUID to %s", config.LastProcessedUUID)
    } else {
        logger.Printf("No accumulated thinking to commit")
    }
    
    // Reset waiting state
    config.WaitingForThinking = false
    config.AccumulatedThinking = nil
    config.WaitingTargetUUID = ""
    
    if err := saveConfig(config); err != nil {
        logger.Printf("Failed to save config: %v", err)
    } else {
        logger.Printf("Config saved successfully after commit")
    }
}

func getCurrentBranch() (string, error) {
    cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
    output, err := cmd.CombinedOutput()
    if err != nil {
        return "", fmt.Errorf("failed to get current branch: %v, output: %s", err, string(output))
    }
    return strings.TrimSpace(string(output)), nil
}

func commitAndMergeBeforeSwitching() error {
    waitMutex.Lock()
    defer waitMutex.Unlock()
    
    // First, commit any accumulated thinking
    if config.WaitingForThinking && len(config.AccumulatedThinking) > 0 {
        logger.Printf("Committing accumulated thinking before switching branches")
        
        // Cancel any pending timer
        if waitTimer != nil {
            waitTimer.Stop()
        }
        
        // Commit the accumulated thinking
        combinedThinking := strings.Join(config.AccumulatedThinking, "\n\n---\n\n")
        if err := gitCommit(combinedThinking); err != nil {
            // Check if the error is just "nothing to commit" - if so, continue to merge
            if strings.Contains(err.Error(), "nothing to commit") {
                logger.Printf("Nothing to commit (working tree clean), continuing to merge")
            } else {
                return fmt.Errorf("failed to commit accumulated thinking: %v", err)
            }
        }
        
        // Update config
        config.LastProcessedUUID = config.WaitingTargetUUID
        config.WaitingForThinking = false
        config.AccumulatedThinking = nil
        config.WaitingTargetUUID = ""
        
        logger.Printf("Committed accumulated thinking and updated config")
    }
    
    // Get current branch
    currentBranch, err := getCurrentBranch()
    if err != nil {
        return fmt.Errorf("failed to get current branch: %v", err)
    }
    
    // If we're already on the main branch, nothing to merge
    if currentBranch == config.MainBranch {
        logger.Printf("Already on main branch (%s), no merge needed", config.MainBranch)
        return nil
    }
    
    logger.Printf("Merging branch %s into %s", currentBranch, config.MainBranch)
    
    // Switch to main branch first
    if err := gitCmd("checkout", config.MainBranch); err != nil {
        return fmt.Errorf("failed to switch to main branch %s: %v", config.MainBranch, err)
    }
    
    // Merge the current branch
    cmd := exec.Command("git", "merge", currentBranch)
    output, err := cmd.CombinedOutput()
    
    if err != nil {
        // Always show merge conflict output, even in non-verbose mode
        logger.Printf("Merge conflict occurred when merging %s into %s:", currentBranch, config.MainBranch)
        logger.Printf("Git merge output: %s", string(output))
        
        if config.QuitOnConflict {
            return fmt.Errorf("merge conflict: %v, output: %s", err, string(output))
        } else {
            logger.Printf("Continuing despite merge conflict (quitonconflict=false)")
            
            // Try to abort the merge to clean up
            abortCmd := exec.Command("git", "merge", "--abort")
            if abortErr := abortCmd.Run(); abortErr != nil {
                logger.Printf("Warning: failed to abort merge: %v", abortErr)
            } else {
                logger.Printf("Merge aborted to clean up state")
            }
        }
    } else {
        logger.Printf("Successfully merged %s into %s", currentBranch, config.MainBranch)
        if config.Verbose {
            logger.Printf("Merge output: %s", string(output))
        }
    }
    
    return nil
}

func gracefulShutdown() {
    logger.Printf("Performing graceful shutdown...")
    
    // Check if we have any pending work to commit and/or need to merge
    shouldCommitAndMerge := false
    
    if config.WaitingForThinking && len(config.AccumulatedThinking) > 0 {
        logger.Printf("Found accumulated thinking, will commit and merge before shutdown")
        shouldCommitAndMerge = true
    } else {
        // Check if we're on a session branch that needs to be merged
        currentBranch, err := getCurrentBranch()
        if err != nil {
            logger.Printf("Warning: Failed to get current branch during shutdown: %v", err)
        } else if currentBranch != config.MainBranch {
            logger.Printf("Currently on session branch %s, will merge to %s before shutdown", currentBranch, config.MainBranch)
            shouldCommitAndMerge = true
        } else {
            logger.Printf("Already on main branch (%s), no merge needed", config.MainBranch)
        }
    }
    
    if shouldCommitAndMerge {
        if err := commitAndMergeBeforeSwitching(); err != nil {
            logger.Printf("Warning: Failed to commit and merge during shutdown: %v", err)
            // Continue with shutdown even if commit/merge fails
        } else {
            logger.Printf("Successfully committed and merged before shutdown")
        }
    }
    
    // Save final config state
    if err := saveConfig(config); err != nil {
        logger.Printf("Warning: Failed to save config during shutdown: %v", err)
    } else {
        logger.Printf("Config saved successfully during shutdown")
    }
    
    logger.Printf("Graceful shutdown completed")
}

