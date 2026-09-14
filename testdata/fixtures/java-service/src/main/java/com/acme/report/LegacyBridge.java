package com.acme.report;

import com.acme.pricing.*;
import com.acme.legacy.*;

// Money existe em com.acme.pricing e em com.acme.legacy: com dois wildcards
// a referência é ambígua de propósito.
public class LegacyBridge {
    public Money convert(long cents) {
        return new Money(cents);
    }
}
