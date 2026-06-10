use std::fmt;
use std::io;

#[derive(Debug)]
pub enum Error {
    InvalidHeader,
    InvalidBlock,
    CrcMismatch,
    KeyNotFound,
    StorageFull,
    CorruptRecord,
    InvalidBlockSize,
    SizeMismatch,
    KeyTooLong,
    Io(io::Error),
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Error::InvalidHeader => write!(f, "embedkv: invalid storage header"),
            Error::InvalidBlock => write!(f, "embedkv: invalid block"),
            Error::CrcMismatch => write!(f, "embedkv: CRC32 mismatch"),
            Error::KeyNotFound => write!(f, "embedkv: key not found"),
            Error::StorageFull => write!(f, "embedkv: storage full"),
            Error::CorruptRecord => write!(f, "embedkv: corrupt record"),
            Error::InvalidBlockSize => write!(f, "embedkv: invalid block size"),
            Error::SizeMismatch => write!(f, "embedkv: storage size mismatch"),
            Error::KeyTooLong => write!(f, "embedkv: key too long for block size"),
            Error::Io(e) => write!(f, "embedkv: io error: {}", e),
        }
    }
}

impl std::error::Error for Error {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            Error::Io(e) => Some(e),
            _ => None,
        }
    }
}

impl From<io::Error> for Error {
    fn from(e: io::Error) -> Self {
        Error::Io(e)
    }
}

pub type Result<T> = std::result::Result<T, Error>;
