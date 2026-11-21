SET LOCAL lock_timeout = '5s';

-- Validating a constraint has nothing to undo; 000056's down drops it.
SELECT 1;
