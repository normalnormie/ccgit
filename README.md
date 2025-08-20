# ccgit - AI Thinking Commit Tool

## Installation

Install the latest version using Go:

```bash
go install github.com/normalnormie/ccgit@latest
```

## Purpose

ccgit is an automated git commit tool that captures an AI assistant's thinking process and uses it as commit messages. It continuously monitors Claude conversation logs and commits AI reasoning to git branches, creating meaningful development history that preserves the thought process behind each change.

## How It Works

### Direct JSONL Monitoring
- Continuously polls Claude conversation logs in configurable directory (default: `~/.claude/projects/[project-name]/`)
- Automatically finds and monitors the latest `.jsonl` file containing AI conversations
- Uses efficient file position tracking to only process new entries
- Polls every 2 seconds for new thinking content
- **Fallback Support**: When using default location, automatically falls back to `~/.config/claude/projects/` if primary location doesn't exist

### Intelligent Session Management
- **Session Detection**: Monitors the most recently modified `.jsonl` file
- **Smart Switching**: When switching between Claude sessions, commits any pending thinking
- **Auto-Merge**: Automatically merges session branches back to main branch before switching
- **Position Tracking**: Sets file position to end when switching, preventing reprocessing of old thinking

### Git Branch Management
- Creates or switches to git branches named after Claude session IDs
- Each Claude conversation gets its own branch for isolated development
- Automatically merges completed sessions back to main branch
- Configurable main branch name (default: "master")

### Smart Commit Logic
- **Thinking Detection**: Extracts "thinking" content from Claude's assistant messages
- **Wait State**: Waits for ONE additional thinking entry before committing
- **30-Second Timeout**: Commits accumulated thinking after timeout if no new thinking arrives
- **Immediate Commit**: If new thinking arrives while waiting, commits immediately
- **Thread-Safe**: Uses mutex synchronization for concurrent operations

## Configuration

The tool uses a `ccgit.conf` file for persistent configuration:

```json
{
  "lastProcessedUUID": "...",       // Tracks last processed thinking entry
  "simulate": false,                // Dry-run mode for testing
  "verbose": false,                 // Enable detailed logging
  "waitingForThinking": false,      // Current waiting state for additional thinking
  "accumulatedThinking": [...],     // Array of thinking entries waiting to be committed
  "waitingTargetUUID": "...",       // UUID to update to after successful commit
  "lastFilePosition": 12345,        // File position of last processed content
  "monitoredFile": "/path/to/file", // Currently monitored JSONL file
  "mainbranch": "master",           // Main branch to merge session branches into
  "quitonconflict": false,          // Whether to quit on merge conflicts (false = continue)
  "projectsdir": "~/.claude/projects/" // Directory where Claude conversation files are stored
}
```

### Configuration Options

- **`projectsdir`**: Directory where Claude conversation files are stored. Defaults to `"~/.claude/projects/"`. Supports tilde expansion. When using the default value, automatically falls back to `~/.config/claude/projects/` if the primary directory doesn't exist.
- **`mainbranch`**: Branch to merge session branches into. Defaults to `"master"`.
- **`quitonconflict`**: Whether to quit on merge conflicts. Defaults to `false` (continue despite conflicts).

## Workflow

### Continuous Monitoring Loop
1. **Startup**: Loads configuration and ensures git repository is initialized
2. **File Discovery**: Finds the latest Claude conversation JSONL file
3. **Session Management**: Handles switching between different Claude sessions
4. **Content Processing**: Reads new content from current file position
5. **Thinking Extraction**: Parses new entries to find AI reasoning content
6. **Commit Logic**: Applies smart waiting and commit strategy
7. **State Persistence**: Updates configuration with progress

### Session Switching Process
1. **Pending Commits**: Commits any accumulated thinking from current session
2. **Branch Merge**: Merges current session branch into main branch
3. **Conflict Handling**: Shows merge conflicts and handles based on configuration
4. **File Switch**: Switches to monitor new session file
5. **Position Reset**: Sets file position to end to ignore existing content
6. **Branch Creation**: Creates or switches to new session branch

### Detailed Commit Process
- **New Thinking Detected**: AI creates new thinking content → ccgit detects it
- **Wait State Activation**: Sets 30-second timer, accumulates thinking
- **Additional Thinking**: If more thinking arrives → immediate commit with all accumulated
- **Timeout Commit**: If 30 seconds pass without new thinking → commit anyway
- **Position Update**: Updates file position to prevent reprocessing

## Key Features

### Core Functionality
- **Zero Configuration**: Automatically creates config file on first run
- **Session Isolation**: Each AI conversation gets its own git branch
- **Smart Session Switching**: Seamlessly handles multiple Claude conversations
- **Automatic Merging**: Merges session work back to main branch
- **Position Tracking**: Efficient processing of only new content

### Reliability & Safety
- **No File System Watching**: Direct JSONL monitoring eliminates file system race conditions  
- **Duplicate Prevention**: Tracks file positions to avoid reprocessing
- **Thread Safety**: Mutex synchronization for concurrent operations
- **Conflict Resolution**: Configurable handling of merge conflicts
- **Timeout Protection**: 30-second timeout ensures commits aren't lost
- **Graceful Shutdown**: Ctrl+C triggers commit and merge of pending thinking before exit

### User Experience
- **Simulation Mode**: Test operations without actual git commits (`"simulate": true`)
- **Verbose Logging**: Detailed output for debugging (`"verbose": true`)
- **Automatic Gitignore**: Adds ccgit.conf and ccgit binary to .gitignore
- **Flexible Branching**: Configurable main branch name
- **Graceful Error Handling**: Continues operation despite minor errors

## Architecture Advantages

### No File System Events
- **Eliminates Race Conditions**: No more missed file writes or duplicate triggers
- **Consistent Polling**: Predictable 2-second polling interval
- **Simpler Logic**: Direct file reading instead of complex event handling
- **Better Reliability**: No dependency on file system notification accuracy

### Smart Session Management
- **Session Continuity**: Returning to previous sessions only processes new content
- **Clean Branch History**: Each session gets committed and merged properly
- **No Lost Work**: Pending thinking always gets committed before switching
- **Conflict Awareness**: User control over merge conflict handling

## Use Case

This tool is designed for developers working with AI assistants (like Claude) who want to maintain a meaningful git history that captures not just *what* changed, but *why* the AI made those changes. Each commit message contains the AI's reasoning, making it easier to understand the development process and debug issues later.

The continuous monitoring approach ensures reliable capture of AI thinking across multiple sessions, while the smart merging strategy keeps the main branch clean and up-to-date with all AI-assisted development work.

## Example Multi-Session Workflow

1. **Session A**: AI thinks about and creates `auth.go` → committed to branch `session-a`
2. **Session Switch**: Switch to new Claude conversation → `session-a` merged to master
3. **Session B**: AI thinks about and creates `database.go` → committed to branch `session-b`  
4. **Return to Session A**: AI adds more to `auth.go` → only NEW thinking processed
5. **Final State**: Both sessions merged to master with complete thinking history

The tool maintains perfect alignment between AI reasoning and code changes across all sessions.
