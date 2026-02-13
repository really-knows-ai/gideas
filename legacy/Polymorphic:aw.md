Here is the breakdown of the universe we are constructing. It is a system built on the pessimistic but accurate assumption that "Don't be a jerk" is a terrible instruction for a computer, and "assert x == 5" is a terrible instruction for a poet.

We are describing a **Polymorphic Governance System**.

In most systems, "Governance" is a PDF that no one reads. In Foundry, Governance is executable code. But—and this is the crucial part—it is not *always* the same kind of code.

Here is the breakdown of the mechanics we have established.

### 1. The Law is Just an Envelope (The MIME Type)

We have established that a "Law" in this system is not a specific data structure; it is a generic container with a label.

* **The Container:** The `Law` CRD.
* **The Content:** A blob of bytes.
* **The Label:** The `spec.type` (MIME type).

This is the most powerful decision in the architecture. It means the Librarian (the database) does not care what the law *is*. It only cares that it exists. It stores Python scripts next to SMT-LIB equations next to angry Markdown notes, with the same indifference a hard drive shows to both Shakespeare and your tax returns.

### 2. Execution is "Eye of the Beholder" (The Node)

Because the Law is just a typed blob, "Execution" is not a global concept. It is a negotiation between the **Law** and the **Node**.

There is no "Law Executor" service. Instead, Nodes query the library for laws they are capable of understanding.

* **The Appraise Node (The Vibe Checker):**
* **Queries for:** `text/markdown`.
* **Execution Method:** It injects the text into an LLM system prompt.
* **Logic:** "The law says 'Be melancholy'. Does this Haiku feel sad? Yes/No."


* **The Quench Node (The Math Nerd):**
* **Queries for:** `application/smt-lib`.
* **Execution Method:** It feeds the content into a Z3 Solver or SMT engine.
* **Logic:** "The law says `(assert (= syllables 17))`. The input has 18. UNSAT."


* **The Python Node (The Script Kiddie):**
* **Queries for:** `application/python` (or `application/wasm`).
* **Execution Method:** It spins up a sandbox and runs the function.
* **Logic:** "The law is a function `def check_compliance(x):`. I ran it, it returned `False`."



**The Unlimited Extensibility:**
This means there is no fixed limit on the "type" of law.
If you want to enforce a law defined by a musical score, you simply:

1. Upload a Law with `type: audio/midi`.
2. Deploy a "Musician Node" that queries for `audio/midi` and "executes" it by checking if the Artefact matches the key signature.

### 3. The Assay Node & The Codification Services

This is the new piece of the puzzle we are clarifying.

When the system deadlocks, or when vague Tier 1 findings ("This code is messy") need to become strict Tier 2 Rulings ("Max complexity is 10"), we need a translation layer. We call this **Codification**.

The **Assay Node** is the Judge, but it is not necessarily the Scribe. It decides *what* the ruling should be, but it may not know the syntax to write it.

Therefore, we introduce **Codification Services**:

* **What they are:** Ephemeral, specialized Docker containers (much like Nodes) that sit alongside the flow.
* **What they do:** They translate Intent (Text) into Syntax (Law).

**The Workflow:**

1. **The Verdict:** The Assay Node jury decides: "We need a law that bans the word 'sausage'."
2. **The Request:** The Assay Node looks at its available Codifiers. It sees a `Codify-SMT` service.
3. **The Job:** It sends the natural language verdict to the `Codify-SMT` service.
4. **The Translation:**
* *Input:* "Ban 'sausage'."
* *Output:* `(assert (not (str.contains artefact "sausage")))`


5. **The Minting:** The Assay Node takes this SMT code and mints a new Tier 2 Law with `type: application/smt-lib`.

### Summary: The Lifecycle of Rigidity

What we are describing is a machine that hardens "Vibes" into "Physics."

1. **Tier 1 (Finding):** "This feels wrong." (Type: `text/markdown`, Executed by LLM).
2. **The Conflict:** "The LLM is inconsistent!"
3. **The Assay:** "Let's make this a hard rule."
4. **The Codification:** The `Codify-SMT` service translates the feeling into math.
5. **Tier 2 (Ruling):** "It is now mathematically impossible to be wrong." (Type: `application/smt-lib`, Executed by Z3).

We have effectively gamified the bureaucracy. We start with soft, fuzzy rules, and through the pain of failure (deadlocks), we crystallize them into hard, executable logic. And the system supports any form of logic, provided you have a container that can run it.