// Define the recursive node structure for type safety during processing
export interface ContextNode {
  [key: string]: boolean | ContextNode | undefined;
}