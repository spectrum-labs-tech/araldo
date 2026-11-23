SET LOCAL lock_timeout = '5s';

-- Validating a constraint has nothing to undo; 000058's down drops it.
SELECT 1;
