use crate::device::BlockDevice;
use crate::error::{Error, Result};
use crate::store::{open, Options, Store};

/// Wraps multiple independent storage replicas with a unified key-value interface.
pub struct ReplicaSet<D: BlockDevice> {
    stores: Vec<Store<D>>,
}

/// Open all devices as replicas (Open → BuildIndex only; no recovery).
pub fn open_replicas<D: BlockDevice>(devices: Vec<D>, opts: Options) -> Result<ReplicaSet<D>> {
    let mut stores: Vec<Store<D>> = Vec::with_capacity(devices.len());
    for dev in devices {
        let mut s = open(dev, opts.clone())?;
        if let Err(e) = s.build_index() {
            s.close();
            close_all(stores);
            return Err(e);
        }
        stores.push(s);
    }
    Ok(ReplicaSet { stores })
}

/// Open all devices as replicas with full recovery (Open → Recover → BuildIndex).
pub fn recover_replicas<D: BlockDevice>(devices: Vec<D>, opts: Options) -> Result<ReplicaSet<D>> {
    let mut stores: Vec<Store<D>> = Vec::with_capacity(devices.len());
    for dev in devices {
        let mut s = open(dev, opts.clone())?;
        if let Err(e) = s.recover() {
            s.close();
            close_all(stores);
            return Err(e);
        }
        if let Err(e) = s.build_index() {
            s.close();
            close_all(stores);
            return Err(e);
        }
        stores.push(s);
    }
    Ok(ReplicaSet { stores })
}

fn close_all<D: BlockDevice>(stores: Vec<Store<D>>) {
    for s in stores {
        s.close();
    }
}

impl<D: BlockDevice> ReplicaSet<D> {
    /// Return the value from the replica holding the highest complete generation.
    pub fn get(&mut self, key: &[u8]) -> Result<Vec<u8>> {
        let _key_str = String::from_utf8_lossy(key).into_owned();
        let mut best_val: Option<Vec<u8>> = None;
        let mut best_gen: u32 = 0;

        for store in &mut self.stores {
            // get_generation_and_value returns (generation, value) if key present.
            let (gen, val) = match store.get_with_generation(key) {
                Some(Ok((g, v))) => (g, v),
                _ => continue,
            };
            if best_val.is_none() || gen > best_gen {
                best_val = Some(val);
                best_gen = gen;
            }
        }

        best_val.ok_or(Error::KeyNotFound)
    }

    /// Write key→value to all replicas sequentially, each with its own flush.
    pub fn put(&mut self, key: &[u8], value: &[u8]) -> Result<()> {
        for store in &mut self.stores {
            store.put(key, value)?;
        }
        Ok(())
    }

    /// Remove the record from all replicas. Tolerates ErrKeyNotFound on individual replicas.
    pub fn delete(&mut self, key: &[u8]) -> Result<()> {
        for store in &mut self.stores {
            match store.delete(key) {
                Ok(()) => {}
                Err(Error::KeyNotFound) => {}
                Err(e) => return Err(e),
            }
        }
        Ok(())
    }

    /// Close all replica stores.
    pub fn close(self) {
        for s in self.stores {
            s.close();
        }
    }
}
