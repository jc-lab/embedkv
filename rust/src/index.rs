use std::collections::HashMap;

/// Tracks the latest complete record for a key.
#[derive(Debug, Clone)]
pub struct IndexEntry {
    pub key: String,
    pub generation: u32,
    pub descriptor_block: u32,
    pub total_size: u32,
}

/// In-memory index mapping key bytes to the latest complete record.
pub struct MemIndex {
    entries: HashMap<String, IndexEntry>,
}

impl Default for MemIndex {
    fn default() -> Self {
        Self::new()
    }
}

impl MemIndex {
    pub fn new() -> Self {
        MemIndex {
            entries: HashMap::new(),
        }
    }

    pub fn put(&mut self, entry: IndexEntry) {
        self.entries.insert(entry.key.clone(), entry);
    }

    pub fn get(&self, key: &str) -> Option<&IndexEntry> {
        self.entries.get(key)
    }

    pub fn delete(&mut self, key: &str) {
        self.entries.remove(key);
    }
}
