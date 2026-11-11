# E2E AIGW Tests

This directory contains end-to-end tests for the AI Gateway CLI functionality.

## Prerequisites

1. **Ollama**: Install and start Ollama locally

   ```bash
   # Start Ollama with a large enough context
   OLLAMA_CONTEXT_LENGTH=131072 OLLAMA_HOST=0.0.0.0 ollama serve
   
   # Pull the test models
   grep _MODEL .env.ollama | cut -d= -f2 | xargs -I{} ollama pull {}
   ```
